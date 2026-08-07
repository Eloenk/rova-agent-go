package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

type Store struct {
	mu          sync.RWMutex
	rules       map[string]*AgentRule
	intents     map[string]*StandingIntent
	executions  []*ExecutionRecord
	supabaseURL string
	supabaseKey string
	httpClient  *http.Client
	realtime    *RealtimeClient
}

func NewStore() *Store {
	return &Store{
		rules:      make(map[string]*AgentRule),
		intents:    make(map[string]*StandingIntent),
		executions: make([]*ExecutionRecord, 0),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func NewSupabaseStore(url, key string) *Store {
	s := NewStore()
	s.supabaseURL = url
	s.supabaseKey = key
	return s
}

func (s *Store) SetSupabaseCredentials(url, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.supabaseURL = url
	s.supabaseKey = key
}

func (s *Store) StartRealtimeSubscription(ctx context.Context) {
	s.mu.Lock()
	url := s.supabaseURL
	key := s.supabaseKey
	s.mu.Unlock()

	if url == "" || key == "" {
		log.Println("[Store] Supabase credentials unconfigured. Realtime disabled.")
		return
	}

	rc := NewRealtimeClient(url, key, s)
	s.mu.Lock()
	s.realtime = rc
	s.mu.Unlock()

	rc.Start(ctx)
}

func (s *Store) syncInitialState() {
	rules, err := s.fetchRulesFromSupabase()
	if err == nil {
		s.mu.Lock()
		for _, r := range rules {
			s.rules[r.ID] = r
		}
		s.mu.Unlock()
		log.Printf("[Realtime State Sync] Synchronized %d active rules into memory cache.", len(rules))
	} else {
		log.Printf("[Realtime State Sync] Error fetching initial rules: %v", err)
	}

	intents, err := s.fetchStandingIntentsFromSupabase()
	if err == nil {
		s.mu.Lock()
		for _, i := range intents {
			s.intents[i.ID] = i
		}
		s.mu.Unlock()
		log.Printf("[Realtime State Sync] Synchronized %d active standing intents into memory cache.", len(intents))
	} else {
		log.Printf("[Realtime State Sync] Error fetching initial standing intents: %v", err)
	}
}

func (s *Store) UpsertRuleFromRealtime(rule *AgentRule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rule.Status == StatusActive {
		s.rules[rule.ID] = rule
	} else {
		delete(s.rules, rule.ID)
	}
}

func (s *Store) RemoveRuleFromRealtime(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rules, id)
}

func (s *Store) UpsertStandingIntentFromRealtime(intent *StandingIntent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if intent.Status == StatusActive {
		s.intents[intent.ID] = intent
	} else {
		delete(s.intents, intent.ID)
	}
}

func (s *Store) RemoveStandingIntentFromRealtime(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.intents, id)
}

type supabaseRuleDto struct {
	ID                  string      `json:"id"`
	CreatedAt           string      `json:"created_at"`
	Status              RuleStatus  `json:"status"`
	RecipientLabel      string      `json:"recipient_label"`
	RecipientIdentifier string      `json:"recipient_identifier"`
	Amount              float64     `json:"amount"`
	Pair                string      `json:"pair"`
	TriggerType         TriggerType `json:"trigger_type"`
	TriggerValue        float64     `json:"trigger_value"`
	CustodyMode         CustodyMode `json:"custody_mode"`
	SourceWallet        string      `json:"source_wallet"`
	NotifyPhone         string      `json:"notify_phone"`
	SourceChannel       string      `json:"source_channel"`
}

func (s *Store) AddRule(rule *AgentRule) {
	s.mu.Lock()
	s.rules[rule.ID] = rule
	s.mu.Unlock()

	if s.supabaseURL != "" && s.supabaseKey != "" {
		reqURL := fmt.Sprintf("%s/rest/v1/agent_rules", s.supabaseURL)
		bodyDto := supabaseRuleDto{
			ID:                  rule.ID,
			CreatedAt:           time.Now().Format(time.RFC3339),
			Status:              rule.Status,
			RecipientLabel:      rule.RecipientLabel,
			RecipientIdentifier: rule.RecipientIdentifier,
			Amount:              rule.Amount,
			Pair:                rule.Pair,
			TriggerType:         rule.TriggerType,
			TriggerValue:        rule.TriggerValue,
			CustodyMode:         rule.CustodyMode,
			SourceWallet:        rule.SourceWallet,
			NotifyPhone:         rule.NotifyPhone,
			SourceChannel:       rule.SourceChannel,
		}
		bodyBytes, _ := json.Marshal(bodyDto)
		req, err := http.NewRequest("POST", reqURL, bytes.NewBuffer(bodyBytes))
		if err == nil {
			req.Header.Set("apikey", s.supabaseKey)
			req.Header.Set("Authorization", "Bearer "+s.supabaseKey)
			req.Header.Set("Content-Type", "application/json")
			resp, err := s.httpClient.Do(req)
			if err == nil {
				defer resp.Body.Close()
			}
		}
	}
}

func (s *Store) GetRule(id string) (*AgentRule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, exists := s.rules[id]
	return r, exists
}

func (s *Store) fetchRulesFromSupabase() ([]*AgentRule, error) {
	reqURL := fmt.Sprintf("%s/rest/v1/agent_rules?status=eq.active", s.supabaseURL)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("apikey", s.supabaseKey)
	req.Header.Set("Authorization", "Bearer "+s.supabaseKey)
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("supabase HTTP %d: %s", resp.StatusCode, string(body))
	}

	var dtos []supabaseRuleDto
	if err := json.NewDecoder(resp.Body).Decode(&dtos); err != nil {
		return nil, err
	}

	rules := make([]*AgentRule, 0, len(dtos))
	for _, d := range dtos {
		rules = append(rules, &AgentRule{
			ID:                  d.ID,
			Status:              d.Status,
			RecipientLabel:      d.RecipientLabel,
			RecipientIdentifier: d.RecipientIdentifier,
			Amount:              d.Amount,
			Pair:                d.Pair,
			TriggerType:         d.TriggerType,
			TriggerValue:        d.TriggerValue,
			CustodyMode:         d.CustodyMode,
			SourceWallet:        d.SourceWallet,
			NotifyPhone:         d.NotifyPhone,
			SourceChannel:       d.SourceChannel,
		})
	}
	return rules, nil
}

func (s *Store) ListActiveRules() []*AgentRule {
	s.mu.RLock()
	url := s.supabaseURL
	key := s.supabaseKey
	s.mu.RUnlock()

	if url != "" && key != "" {
		rules, err := s.fetchRulesFromSupabase()
		if err == nil {
			return rules
		}
		log.Printf("[Store] Supabase fetch error, falling back to memory: %v", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	active := make([]*AgentRule, 0)
	for _, r := range s.rules {
		if r.Status == StatusActive {
			active = append(active, r)
		}
	}
	return active
}

func (s *Store) UpdateRuleStatus(id string, status RuleStatus) {
	s.mu.Lock()
	if r, exists := s.rules[id]; exists {
		r.Status = status
	}
	url := s.supabaseURL
	key := s.supabaseKey
	s.mu.Unlock()

	if url != "" && key != "" {
		reqURL := fmt.Sprintf("%s/rest/v1/agent_rules?id=eq.%s", url, id)
		bodyBytes, _ := json.Marshal(map[string]string{"status": string(status)})
		req, err := http.NewRequest("PATCH", reqURL, bytes.NewBuffer(bodyBytes))
		if err != nil {
			log.Printf("[Store] Error building PATCH request: %v", err)
			return
		}
		req.Header.Set("apikey", key)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")

		resp, err := s.httpClient.Do(req)
		if err != nil {
			log.Printf("[Store] Error updating rule status in Supabase: %v", err)
			return
		}
		defer resp.Body.Close()
	}
}

type supabaseStandingIntentDto struct {
	ID               string                 `json:"id"`
	CreatedAt        string                 `json:"created_at"`
	Status           RuleStatus             `json:"status"`
	IntentText       string                 `json:"intent_text"`
	Plan             StandingIntentPlanStep `json:"plan"`
	Trigger          StandingIntentTrigger  `json:"trigger"`
	CustodyMode      CustodyMode            `json:"custody_mode"`
	SourceWallet     string                 `json:"source_wallet"`
	LastKnownBalance float64                `json:"last_known_balance"`
	LastRunAt        *string                `json:"last_run_at,omitempty"`
	RunCount         int                    `json:"run_count"`
	NotifyPhone      string                 `json:"notify_phone,omitempty"`
	SourceChannel    string                 `json:"source_channel,omitempty"`
}

func (s *Store) AddStandingIntent(intent *StandingIntent) {
	s.mu.Lock()
	s.intents[intent.ID] = intent
	url := s.supabaseURL
	key := s.supabaseKey
	s.mu.Unlock()

	if url != "" && key != "" {
		reqURL := fmt.Sprintf("%s/rest/v1/standing_intents", url)
		bodyDto := supabaseStandingIntentDto{
			ID:               intent.ID,
			CreatedAt:        time.Now().Format(time.RFC3339),
			Status:           intent.Status,
			IntentText:       intent.IntentText,
			Plan:             intent.Plan,
			Trigger:          intent.Trigger,
			CustodyMode:      intent.CustodyMode,
			SourceWallet:     intent.SourceWallet,
			LastKnownBalance: intent.LastKnownBalance,
			RunCount:         intent.RunCount,
			NotifyPhone:      intent.NotifyPhone,
			SourceChannel:    intent.SourceChannel,
		}
		bodyBytes, _ := json.Marshal(bodyDto)
		req, err := http.NewRequest("POST", reqURL, bytes.NewBuffer(bodyBytes))
		if err == nil {
			req.Header.Set("apikey", key)
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")
			resp, err := s.httpClient.Do(req)
			if err == nil {
				defer resp.Body.Close()
			}
		}
	}
}

func (s *Store) fetchStandingIntentsFromSupabase() ([]*StandingIntent, error) {
	reqURL := fmt.Sprintf("%s/rest/v1/standing_intents?status=eq.active", s.supabaseURL)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("apikey", s.supabaseKey)
	req.Header.Set("Authorization", "Bearer "+s.supabaseKey)
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("supabase HTTP %d: %s", resp.StatusCode, string(body))
	}

	var dtos []supabaseStandingIntentDto
	if err := json.NewDecoder(resp.Body).Decode(&dtos); err != nil {
		return nil, err
	}

	intents := make([]*StandingIntent, 0, len(dtos))
	for _, d := range dtos {
		var lastRunAt *time.Time
		if d.LastRunAt != nil {
			if t, err := time.Parse(time.RFC3339, *d.LastRunAt); err == nil {
				lastRunAt = &t
			}
		}

		t, _ := time.Parse(time.RFC3339, d.CreatedAt)
		intents = append(intents, &StandingIntent{
			ID:               d.ID,
			CreatedAt:        t,
			Status:           d.Status,
			IntentText:       d.IntentText,
			Plan:             d.Plan,
			Trigger:          d.Trigger,
			CustodyMode:      d.CustodyMode,
			SourceWallet:     d.SourceWallet,
			LastKnownBalance: d.LastKnownBalance,
			LastRunAt:        lastRunAt,
			RunCount:         d.RunCount,
			NotifyPhone:      d.NotifyPhone,
			SourceChannel:    d.SourceChannel,
		})
	}
	return intents, nil
}

func (s *Store) ListActiveStandingIntents() []*StandingIntent {
	s.mu.RLock()
	url := s.supabaseURL
	key := s.supabaseKey
	s.mu.RUnlock()

	if url != "" && key != "" {
		intents, err := s.fetchStandingIntentsFromSupabase()
		if err == nil {
			return intents
		}
		log.Printf("[Store] Supabase standing_intents fetch error: %v", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	active := make([]*StandingIntent, 0)
	for _, i := range s.intents {
		if i.Status == StatusActive {
			active = append(active, i)
		}
	}
	return active
}

func (s *Store) UpdateStandingIntentState(id string, lastKnownBalance float64, runCount int, lastRunAt time.Time) {
	s.mu.Lock()
	if i, exists := s.intents[id]; exists {
		i.LastKnownBalance = lastKnownBalance
		i.RunCount = runCount
		i.LastRunAt = &lastRunAt
	}
	url := s.supabaseURL
	key := s.supabaseKey
	s.mu.Unlock()

	if url != "" && key != "" {
		reqURL := fmt.Sprintf("%s/rest/v1/standing_intents?id=eq.%s", url, id)
		bodyBytes, _ := json.Marshal(map[string]interface{}{
			"last_known_balance": lastKnownBalance,
			"run_count":          runCount,
			"last_run_at":        lastRunAt.Format(time.RFC3339),
		})
		req, err := http.NewRequest("PATCH", reqURL, bytes.NewBuffer(bodyBytes))
		if err != nil {
			return
		}
		req.Header.Set("apikey", key)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")

		resp, err := s.httpClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
		}
	}
}

func (s *Store) RecordExecution(exec *ExecutionRecord) {
	s.mu.Lock()
	s.executions = append([]*ExecutionRecord{exec}, s.executions...)
	url := s.supabaseURL
	key := s.supabaseKey
	s.mu.Unlock()

	if url != "" && key != "" {
		reqURL := fmt.Sprintf("%s/rest/v1/agent_executions", url)
		bodyData := map[string]interface{}{
			"id":                exec.ID,
			"rule_id":           exec.RuleID,
			"fired_at":          exec.FiredAt.Format(time.RFC3339),
			"rate_at_execution": exec.RateAtExecution,
			"mode":              "real",
			"tx_hash":           exec.TxHash,
			"arc_scan_url":      exec.ArcScanURL,
			"fee_amount_usdc":   0.05,
			"memo":              exec.Memo,
		}
		bodyBytes, _ := json.Marshal(bodyData)
		req, err := http.NewRequest("POST", reqURL, bytes.NewBuffer(bodyBytes))
		if err == nil {
			req.Header.Set("apikey", key)
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")
			resp, err := s.httpClient.Do(req)
			if err == nil {
				defer resp.Body.Close()
			}
		}
	}
}

func (s *Store) ListExecutions() []*ExecutionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.executions
}
