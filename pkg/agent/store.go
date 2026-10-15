package agent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Store struct {
	mu          sync.RWMutex
	rules       map[string]*AgentRule
	intents     map[string]*StandingIntent
	executions  []*ExecutionRecord
	supabaseURL string
	supabaseKey string
	realtimeKey string
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

func NewSupabaseStore(url, key, realtimeKey string) *Store {
	s := NewStore()
	s.supabaseURL = url
	s.supabaseKey = key
	s.realtimeKey = realtimeKey
	return s
}

func (s *Store) SetSupabaseCredentials(url, key, realtimeKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.supabaseURL = url
	s.supabaseKey = key
	s.realtimeKey = realtimeKey
}

func (s *Store) newSupabaseRequest(ctx context.Context, method, resource string, query url.Values, payload interface{}) (*http.Request, error) {
	s.mu.RLock()
	baseURL := s.supabaseURL
	secretKey := s.supabaseKey
	s.mu.RUnlock()
	if baseURL == "" || secretKey == "" {
		return nil, fmt.Errorf("Supabase service credentials are unavailable")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("Supabase URL is invalid")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/rest/v1/" + strings.TrimLeft(resource, "/")
	parsed.RawQuery = query.Encode()

	var body io.Reader
	if payload != nil {
		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to encode Supabase request")
		}
		body = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), body)
	if err != nil {
		return nil, fmt.Errorf("failed to create Supabase request")
	}
	req.Header.Set("apikey", secretKey)
	req.Header.Set("Authorization", "Bearer "+secretKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (s *Store) claim(ctx context.Context, functionName, id, ownerEmail string) (json.RawMessage, error) {
	req, err := s.newSupabaseRequest(ctx, http.MethodPost, "rpc/"+functionName, nil, map[string]string{
		"requested_id":          id,
		"requested_owner_email": ownerEmail,
	})
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Supabase claim request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Supabase claim returned HTTP %d", resp.StatusCode)
	}

	var claimed []json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&claimed); err != nil {
		return nil, fmt.Errorf("failed to decode Supabase claim response")
	}
	if len(claimed) == 0 {
		return nil, nil
	}
	if len(claimed) != 1 {
		return nil, fmt.Errorf("Supabase claim returned an unexpected record count")
	}

	s.mu.Lock()
	delete(s.rules, id)
	delete(s.intents, id)
	s.mu.Unlock()
	return claimed[0], nil
}

func (s *Store) TryClaimRule(ctx context.Context, id, ownerEmail string) (*AgentRule, error) {
	raw, err := s.claim(ctx, "claim_agent_rule", id, ownerEmail)
	if err != nil || raw == nil {
		return nil, err
	}
	var dto supabaseRuleDto
	if err := json.Unmarshal(raw, &dto); err != nil || dto.ID != id {
		return nil, fmt.Errorf("Supabase rule claim returned an invalid record")
	}
	return agentRuleFromDTO(dto), nil
}

func (s *Store) TryClaimStandingIntent(ctx context.Context, id, ownerEmail string) (*StandingIntent, error) {
	raw, err := s.claim(ctx, "claim_standing_intent", id, ownerEmail)
	if err != nil || raw == nil {
		return nil, err
	}
	var dto supabaseStandingIntentDto
	if err := json.Unmarshal(raw, &dto); err != nil || dto.ID != id {
		return nil, fmt.Errorf("Supabase standing-intent claim returned an invalid record")
	}
	return standingIntentFromDTO(dto), nil
}

func (s *Store) patchOne(ctx context.Context, resource, id string, expectedStatus RuleStatus, payload interface{}) error {
	query := url.Values{"id": {"eq." + id}}
	if expectedStatus != "" {
		query.Set("status", "eq."+string(expectedStatus))
	}
	req, err := s.newSupabaseRequest(ctx, http.MethodPatch, resource, query, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Prefer", "return=representation")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("Supabase update request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Supabase update returned HTTP %d", resp.StatusCode)
	}

	var updated []json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&updated); err != nil {
		return fmt.Errorf("failed to decode Supabase update response")
	}
	if len(updated) != 1 {
		return fmt.Errorf("Supabase update did not affect exactly one record")
	}
	return nil
}

func (s *Store) StartRealtimeSubscription(ctx context.Context) {
	s.mu.Lock()
	url := s.supabaseURL
	serviceKey := s.supabaseKey
	realtimeKey := s.realtimeKey
	s.mu.Unlock()

	if url == "" || serviceKey == "" {
		log.Println("[Store] Supabase service credentials unconfigured; state synchronization is disabled")
		return
	}

	s.syncInitialState()
	go s.pollState(ctx, 30*time.Second)
	if realtimeKey == "" {
		log.Println("[Store] Supabase publishable Realtime key unconfigured; using periodic REST synchronization")
		return
	}

	rc := NewRealtimeClient(url, realtimeKey, s)
	s.mu.Lock()
	s.realtime = rc
	s.mu.Unlock()

	rc.Start(ctx)
}

func (s *Store) pollState(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncInitialState()
		}
	}
}

func (s *Store) syncInitialState() {
	rules, err := s.fetchRulesFromSupabase()
	if err == nil {
		nextRules := make(map[string]*AgentRule, len(rules))
		for _, rule := range rules {
			nextRules[rule.ID] = rule
		}
		s.mu.Lock()
		s.rules = nextRules
		s.mu.Unlock()
		log.Printf("[Realtime State Sync] Synchronized %d active rules into memory cache.", len(rules))
	} else {
		log.Printf("[Realtime State Sync] Error fetching initial rules: %v", err)
	}

	intents, err := s.fetchStandingIntentsFromSupabase()
	if err == nil {
		nextIntents := make(map[string]*StandingIntent, len(intents))
		for _, intent := range intents {
			nextIntents[intent.ID] = intent
		}
		s.mu.Lock()
		s.intents = nextIntents
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
	OwnerEmail          string      `json:"owner_email"`
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

func agentRuleFromDTO(dto supabaseRuleDto) *AgentRule {
	createdAt, _ := time.Parse(time.RFC3339, dto.CreatedAt)
	return &AgentRule{
		ID:                  dto.ID,
		CreatedAt:           createdAt,
		Status:              dto.Status,
		OwnerEmail:          dto.OwnerEmail,
		RecipientLabel:      dto.RecipientLabel,
		RecipientIdentifier: dto.RecipientIdentifier,
		Amount:              dto.Amount,
		Pair:                dto.Pair,
		TriggerType:         dto.TriggerType,
		TriggerValue:        dto.TriggerValue,
		CustodyMode:         dto.CustodyMode,
		SourceWallet:        dto.SourceWallet,
		NotifyPhone:         dto.NotifyPhone,
		SourceChannel:       dto.SourceChannel,
	}
}

func (s *Store) GetRule(id string) (*AgentRule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, exists := s.rules[id]
	return r, exists
}

func (s *Store) fetchRulesFromSupabase() ([]*AgentRule, error) {
	req, err := s.newSupabaseRequest(context.Background(), http.MethodGet, "agent_rules", url.Values{"status": {"eq.active"}}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build Supabase rule request")
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Supabase rule fetch request failed")
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("supabase HTTP %d", resp.StatusCode)
	}

	var dtos []supabaseRuleDto
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&dtos); err != nil {
		return nil, fmt.Errorf("failed to decode Supabase rule response")
	}

	rules := make([]*AgentRule, 0, len(dtos))
	for _, dto := range dtos {
		rules = append(rules, agentRuleFromDTO(dto))
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
		log.Printf("[Store] Supabase rule fetch failed; returning no executable rules")
		return []*AgentRule{}
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

func (s *Store) UpdateRuleStatus(ctx context.Context, id string, status RuleStatus) error {
	if err := s.patchOne(ctx, "agent_rules", id, StatusReadyToExecute, map[string]string{"status": string(status)}); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if status == StatusActive {
		if rule, exists := s.rules[id]; exists {
			rule.Status = status
		}
	} else {
		delete(s.rules, id)
	}
	return nil
}

type supabaseStandingIntentDto struct {
	ID               string                 `json:"id"`
	CreatedAt        string                 `json:"created_at"`
	Status           RuleStatus             `json:"status"`
	OwnerEmail       string                 `json:"owner_email"`
	IntentText       string                 `json:"intent_text"`
	Plan             StandingIntentPlanStep `json:"plan"`
	Trigger          StandingIntentTrigger  `json:"trigger"`
	CustodyMode      CustodyMode            `json:"custody_mode"`
	SourceWallet     string                 `json:"source_wallet"`
	LastKnownBalance *float64               `json:"last_known_balance"`
	LastRunAt        *string                `json:"last_run_at,omitempty"`
	RunCount         int                    `json:"run_count"`
	NotifyPhone      string                 `json:"notify_phone,omitempty"`
	SourceChannel    string                 `json:"source_channel,omitempty"`
}

func standingIntentFromDTO(dto supabaseStandingIntentDto) *StandingIntent {
	var lastRunAt *time.Time
	if dto.LastRunAt != nil {
		if parsed, err := time.Parse(time.RFC3339, *dto.LastRunAt); err == nil {
			lastRunAt = &parsed
		}
	}
	createdAt, _ := time.Parse(time.RFC3339, dto.CreatedAt)
	return &StandingIntent{
		ID:               dto.ID,
		CreatedAt:        createdAt,
		Status:           dto.Status,
		OwnerEmail:       dto.OwnerEmail,
		IntentText:       dto.IntentText,
		Plan:             dto.Plan,
		Trigger:          dto.Trigger,
		CustodyMode:      dto.CustodyMode,
		SourceWallet:     dto.SourceWallet,
		LastKnownBalance: dto.LastKnownBalance,
		LastRunAt:        lastRunAt,
		RunCount:         dto.RunCount,
		NotifyPhone:      dto.NotifyPhone,
		SourceChannel:    dto.SourceChannel,
	}
}

func (s *Store) fetchStandingIntentsFromSupabase() ([]*StandingIntent, error) {
	req, err := s.newSupabaseRequest(context.Background(), http.MethodGet, "standing_intents", url.Values{"status": {"eq.active"}}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build Supabase standing-intent request")
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Supabase standing-intent fetch request failed")
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("supabase HTTP %d", resp.StatusCode)
	}

	var dtos []supabaseStandingIntentDto
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&dtos); err != nil {
		return nil, fmt.Errorf("failed to decode Supabase standing-intent response")
	}

	intents := make([]*StandingIntent, 0, len(dtos))
	for _, dto := range dtos {
		intents = append(intents, standingIntentFromDTO(dto))
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
		log.Printf("[Store] Supabase standing-intent fetch failed; returning no executable intents")
		return []*StandingIntent{}
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

func (s *Store) UpdateStandingIntentBaseline(ctx context.Context, id string, lastKnownBalance float64) error {
	if err := s.patchOne(ctx, "standing_intents", id, StatusActive, map[string]interface{}{
		"last_known_balance": lastKnownBalance,
	}); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if intent, exists := s.intents[id]; exists {
		intent.LastKnownBalance = &lastKnownBalance
	}
	return nil
}

func (s *Store) CompleteStandingIntentRun(ctx context.Context, id string, lastKnownBalance float64, runCount int, lastRunAt time.Time) error {
	if err := s.patchOne(ctx, "standing_intents", id, StatusReadyToExecute, map[string]interface{}{
		"status":             string(StatusActive),
		"last_known_balance": lastKnownBalance,
		"run_count":          runCount,
		"last_run_at":        lastRunAt.Format(time.RFC3339),
	}); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if intent, exists := s.intents[id]; exists {
		intent.Status = StatusActive
		intent.LastKnownBalance = &lastKnownBalance
		intent.RunCount = runCount
		intent.LastRunAt = &lastRunAt
	}
	return nil
}

func (s *Store) RecordExecution(ctx context.Context, exec *ExecutionRecord) error {
	if exec.OwnerEmail == "" || exec.TxHash == "" || exec.ArcScanURL == "" {
		return fmt.Errorf("execution receipt is missing required fields")
	}
	if !strings.HasPrefix(exec.TxHash, "0x") {
		return fmt.Errorf("execution receipt transaction hash is invalid")
	}
	cleanHash := strings.TrimPrefix(exec.TxHash, "0x")
	if len(cleanHash) != 64 {
		return fmt.Errorf("execution receipt transaction hash is invalid")
	}
	if _, err := hex.DecodeString(cleanHash); err != nil {
		return fmt.Errorf("execution receipt transaction hash is invalid")
	}
	if (exec.RuleID == "") == (exec.StandingIntentID == "") {
		return fmt.Errorf("execution receipt must reference exactly one rule or standing intent")
	}

	if exec.ID == "" {
		exec.ID = "exec_" + uuid.NewString()
	}

	var rateAtExecution interface{}
	if exec.RateAtExecution > 0 {
		rateAtExecution = exec.RateAtExecution
	}
	bodyData := map[string]interface{}{
		"id":                 exec.ID,
		"owner_email":        exec.OwnerEmail,
		"rule_id":            nil,
		"standing_intent_id": nil,
		"fired_at":           exec.FiredAt.Format(time.RFC3339),
		"rate_at_execution":  rateAtExecution,
		"mode":               "real",
		"tx_hash":            exec.TxHash,
		"arc_scan_url":       exec.ArcScanURL,
		"fee_amount_usdc":    0,
		"memo":               exec.Memo,
		"quote_shop":         exec.QuoteShop,
	}
	if exec.RuleID != "" {
		bodyData["rule_id"] = exec.RuleID
	} else {
		bodyData["standing_intent_id"] = exec.StandingIntentID
	}

	req, err := s.newSupabaseRequest(ctx, http.MethodPost, "agent_executions", nil, bodyData)
	if err != nil {
		return err
	}
	req.Header.Set("Prefer", "return=minimal")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("Supabase execution receipt request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Supabase execution receipt returned HTTP %d", resp.StatusCode)
	}

	s.mu.Lock()
	s.executions = append([]*ExecutionRecord{exec}, s.executions...)
	s.mu.Unlock()
	return nil
}

func (s *Store) ListExecutions() []*ExecutionRecord {
	s.mu.RLock()
	configured := s.supabaseURL != "" && s.supabaseKey != ""
	s.mu.RUnlock()
	if configured {
		executions, err := s.fetchExecutionsFromSupabase()
		if err != nil {
			log.Printf("[Store] Supabase execution fetch failed; returning no receipts")
			return []*ExecutionRecord{}
		}
		return executions
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]*ExecutionRecord(nil), s.executions...)
}

type supabaseExecutionDto struct {
	ID               string           `json:"id"`
	OwnerEmail       string           `json:"owner_email"`
	RuleID           *string          `json:"rule_id"`
	StandingIntentID *string          `json:"standing_intent_id"`
	FiredAt          string           `json:"fired_at"`
	RateAtExecution  *float64         `json:"rate_at_execution"`
	TxHash           string           `json:"tx_hash"`
	ArcScanURL       string           `json:"arc_scan_url"`
	Memo             string           `json:"memo"`
	QuoteShop        *QuoteShopResult `json:"quote_shop"`
}

func (s *Store) fetchExecutionsFromSupabase() ([]*ExecutionRecord, error) {
	query := url.Values{
		"order": {"fired_at.desc"},
		"limit": {"100"},
	}
	req, err := s.newSupabaseRequest(context.Background(), http.MethodGet, "agent_executions", query, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Supabase execution fetch request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Supabase execution fetch returned HTTP %d", resp.StatusCode)
	}

	var rows []supabaseExecutionDto
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rows); err != nil {
		return nil, fmt.Errorf("failed to decode Supabase execution response")
	}
	executions := make([]*ExecutionRecord, 0, len(rows))
	for _, row := range rows {
		firedAt, err := time.Parse(time.RFC3339, row.FiredAt)
		if err != nil {
			continue
		}
		execution := &ExecutionRecord{
			ID:         row.ID,
			OwnerEmail: row.OwnerEmail,
			FiredAt:    firedAt,
			TxHash:     row.TxHash,
			ArcScanURL: row.ArcScanURL,
			Memo:       row.Memo,
			QuoteShop:  row.QuoteShop,
		}
		if row.RuleID != nil {
			execution.RuleID = *row.RuleID
		}
		if row.StandingIntentID != nil {
			execution.StandingIntentID = *row.StandingIntentID
		}
		if row.RateAtExecution != nil {
			execution.RateAtExecution = *row.RateAtExecution
		}
		executions = append(executions, execution)
	}
	return executions, nil
}
