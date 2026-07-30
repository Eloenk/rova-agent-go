package agent

import (
	"bytes"
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

func (s *Store) AddStandingIntent(intent *StandingIntent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.intents[intent.ID] = intent
}

func (s *Store) ListActiveStandingIntents() []*StandingIntent {
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
