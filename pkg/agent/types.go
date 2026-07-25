package agent

import "time"

type RuleStatus string
type CustodyMode string
type TriggerType string

const (
	StatusActive         RuleStatus = "active"
	StatusReadyToExecute RuleStatus = "ready_to_execute"
	StatusFired          RuleStatus = "fired"
	StatusCancelled      RuleStatus = "cancelled"
)

const (
	CustodyManaged     CustodyMode = "managed"
	CustodySelfCustody CustodyMode = "self_custody"
)

const (
	TriggerRateGTE TriggerType = "rate_gte"
	TriggerRateLTE TriggerType = "rate_lte"
	TriggerByDate  TriggerType = "by_date"
)

type AgentRule struct {
	ID                  string      `json:"id"`
	CreatedAt           time.Time   `json:"createdAt"`
	Status              RuleStatus  `json:"status"`
	RecipientLabel      string      `json:"recipientLabel"`
	RecipientIdentifier string      `json:"recipientIdentifier"`
	Amount              float64     `json:"amount"`
	Pair                string      `json:"pair"`
	TriggerType         TriggerType `json:"triggerType"`
	TriggerValue        float64     `json:"triggerValue"`
	CustodyMode         CustodyMode `json:"custodyMode"`
	SourceWallet        string      `json:"sourceWallet"`
	NotifyPhone         string      `json:"notifyPhone,omitempty"`
	SourceChannel       string      `json:"sourceChannel,omitempty"`
}

type StandingIntent struct {
	ID           string      `json:"id"`
	CreatedAt    time.Time   `json:"createdAt"`
	Status       RuleStatus  `json:"status"`
	IntentText   string      `json:"intentText"`
	Interval     string      `json:"interval"`
	CustodyMode  CustodyMode `json:"custodyMode"`
	SourceWallet string      `json:"sourceWallet"`
	NotifyPhone  string      `json:"notifyPhone,omitempty"`
}

type QuoteResult struct {
	Provider string  `json:"provider"`
	Rate     float64 `json:"rate"`
	PaidUsdc float64 `json:"paidUsdc"`
}

type QuoteShopResult struct {
	ProvidersChecked int           `json:"providersChecked"`
	BestProvider     string        `json:"bestProvider"`
	BestRate         float64       `json:"bestRate"`
	Quotes           []QuoteResult `json:"quotes"`
}

type ExecutionRecord struct {
	ID              string           `json:"id"`
	RuleID          string           `json:"ruleId"`
	FiredAt         time.Time        `json:"firedAt"`
	RateAtExecution float64          `json:"rateAtExecution"`
	TxHash          string           `json:"txHash"`
	ArcScanURL      string           `json:"arcScanUrl"`
	Memo            string           `json:"memo"`
	QuoteShop       *QuoteShopResult `json:"quoteShop,omitempty"`
}
