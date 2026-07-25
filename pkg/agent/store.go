package agent

import (
	"sync"
)

type Store struct {
	mu         sync.RWMutex
	rules      map[string]*AgentRule
	intents    map[string]*StandingIntent
	executions []*ExecutionRecord
}

func NewStore() *Store {
	return &Store{
		rules:      make(map[string]*AgentRule),
		intents:    make(map[string]*StandingIntent),
		executions: make([]*ExecutionRecord, 0),
	}
}

func (s *Store) AddRule(rule *AgentRule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules[rule.ID] = rule
}

func (s *Store) GetRule(id string) (*AgentRule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, exists := s.rules[id]
	return r, exists
}

func (s *Store) ListActiveRules() []*AgentRule {
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
	defer s.mu.Unlock()
	if r, exists := s.rules[id]; exists {
		r.Status = status
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
	defer s.mu.Unlock()
	s.executions = append([]*ExecutionRecord{exec}, s.executions...)
}

func (s *Store) ListExecutions() []*ExecutionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.executions
}
