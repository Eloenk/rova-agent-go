package agent

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"rova-agent-go/pkg/chain"
	"rova-agent-go/pkg/config"
	"rova-agent-go/pkg/nanopay"
)

type NotificationOpts struct {
	Recipient        string
	Amount           float64
	Pair             string
	Rate             float64
	BestProvider     string
	ProvidersChecked int
	TxHash           string
	ArcScanURL       string
	Memo             string
}

type Notifier interface {
	SendMessage(toPhone, text string) error
	SendExecutionReport(toPhone string, opts NotificationOpts) error
	SendApprovalAlert(toPhone, ruleID, recipient string, amount, rate float64) error
}

type WatcherEngine struct {
	Store       *Store
	ChainClient *chain.ChainClient
	Shopper     *nanopay.Shopper
	Notifier    Notifier
	Interval    time.Duration
}

func NewWatcherEngine(store *Store, chainClient *chain.ChainClient, shopper *nanopay.Shopper, notifier Notifier, interval time.Duration) *WatcherEngine {
	return &WatcherEngine{
		Store:       store,
		ChainClient: chainClient,
		Shopper:     shopper,
		Notifier:    notifier,
		Interval:    interval,
	}
}

func (w *WatcherEngine) getActiveTargetWallets() []string {
	walletMap := make(map[string]bool)
	for _, intent := range w.Store.ListActiveStandingIntents() {
		if intent.SourceWallet != "" {
			walletMap[strings.ToLower(intent.SourceWallet)] = true
		}
	}
	for _, rule := range w.Store.ListActiveRules() {
		if rule.SourceWallet != "" {
			walletMap[strings.ToLower(rule.SourceWallet)] = true
		}
	}
	wallets := make([]string, 0, len(walletMap))
	for wallet := range walletMap {
		wallets = append(wallets, wallet)
	}
	return wallets
}

func (w *WatcherEngine) StartWatcher(ctx context.Context) {
	ticker := time.NewTicker(w.Interval)
	evaluationRequests := make(chan struct{}, 1)
	requestEvaluation := func() {
		select {
		case evaluationRequests <- struct{}{}:
		default:
		}
	}

	// Mode 0: Initialize Supabase Realtime WebSocket Push Subscription for Rules & Intents
	if w.Store != nil {
		log.Println("[Watcher Engine] Initializing Supabase Realtime WebSocket (wss://) Subscription...")
		w.Store.StartRealtimeSubscription(ctx)
	}

	// Mode 1: Real-time WSS Event Subscription
	if w.ChainClient != nil && w.ChainClient.Config != nil && w.ChainClient.Config.BalanceMode == "wss" {
		log.Println("[Watcher Engine] Initializing Real-Time WSS Transfer Event Listener for Active Rule Wallets...")
		w.ChainClient.ListenUSDCTransferEvents(ctx, w.getActiveTargetWallets, func(toAddress string, amount float64, txHash string) {
			log.Printf("[Watcher Engine] Monitored transfer event received; scheduling a database-claimed balance evaluation")
			requestEvaluation()
		})
	} else {
		log.Println("[Watcher Engine] Running in Multi-RPC Failover Polling Mode")
	}

	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.evaluateRules(ctx)
				w.evaluateStandingIntents(ctx)
			case <-evaluationRequests:
				w.evaluateStandingIntents(ctx)
			}
		}
	}()
}

func standingSavingsAmount(intent *StandingIntent, balance float64) (float64, float64, bool) {
	if intent.LastKnownBalance == nil || intent.Trigger.Type != "on_receive" || !strings.EqualFold(intent.Plan.Action, "save") {
		return 0, 0, false
	}
	usesPercentage := intent.Plan.Percentage > 0 && intent.Plan.Percentage <= 100
	usesFixedAmount := intent.Plan.Amount > 0
	if usesPercentage == usesFixedAmount {
		return 0, 0, false
	}
	delta := balance - *intent.LastKnownBalance
	if math.IsNaN(delta) || math.IsInf(delta, 0) {
		return 0, 0, false
	}
	minimum := intent.Trigger.MinAmountUsdc
	if minimum <= 0 {
		minimum = 0.01
	}
	if math.IsNaN(minimum) || math.IsInf(minimum, 0) || delta < minimum {
		return 0, 0, false
	}

	saveAmount := intent.Plan.Amount
	if usesPercentage {
		saveAmount = math.Floor(delta*(intent.Plan.Percentage/100.0)*1e6) / 1e6
	} else {
		scaled := math.Round(saveAmount * 1e6)
		if math.Abs(saveAmount*1e6-scaled) > 1e-6 {
			return 0, 0, false
		}
		saveAmount = scaled / 1e6
	}
	if saveAmount <= 0 || saveAmount > delta {
		return 0, 0, false
	}
	return delta, saveAmount, true
}

func (w *WatcherEngine) evaluateStandingIntents(ctx context.Context) {
	intents := w.Store.ListActiveStandingIntents()
	for _, intent := range intents {
		if intent.SourceWallet == "" || intent.OwnerEmail == "" {
			continue
		}
		if intent.CustodyMode != CustodyManaged || !w.canExecuteAutomatically(0) {
			continue
		}
		if w.ChainClient.Config.ExecutionMode != "circle" {
			continue
		}

		balance, err := w.ChainClient.GetBalanceUSDCWithFailover(ctx, intent.SourceWallet)
		if err != nil {
			continue
		}

		if intent.LastKnownBalance == nil {
			if err := w.Store.UpdateStandingIntentBaseline(ctx, intent.ID, balance); err != nil {
				log.Printf("[Watcher] Standing intent %s baseline persistence failed", intent.ID)
			}
			continue
		}

		_, saveAmount, qualifies := standingSavingsAmount(intent, balance)
		if !qualifies || !w.canExecuteAutomatically(saveAmount) {
			continue
		}

		claimedIntent, err := w.Store.TryClaimStandingIntent(ctx, intent.ID, intent.OwnerEmail)
		if err != nil {
			log.Printf("[Watcher] Standing intent %s claim failed", intent.ID)
			continue
		}
		if claimedIntent == nil {
			continue
		}
		intent = claimedIntent

		log.Printf("[Watcher] Standing intent %s claimed for an approved savings execution", intent.ID)
		balance, err = w.ChainClient.GetBalanceUSDCWithFailover(ctx, intent.SourceWallet)
		if err != nil {
			log.Printf("[Watcher] Standing intent %s balance recheck failed and remains paused", intent.ID)
			continue
		}
		delta, saveAmount, qualifies := standingSavingsAmount(intent, balance)
		if !qualifies || !w.canExecuteAutomatically(saveAmount) {
			log.Printf("[Watcher] Standing intent %s no longer satisfies its claimed policy and remains paused", intent.ID)
			continue
		}
		txHash, err := w.ChainClient.CircleClient.DepositSavingsVault(ctx, intent.SourceWallet, saveAmount)
		if err != nil {
			log.Printf("[Watcher] Standing intent %s execution failed and remains paused for review", intent.ID)
			continue
		}

		firedAt := time.Now()
		newRunCount := intent.RunCount + 1
		newKnownBalance := balance - saveAmount
		arcScanURL := fmt.Sprintf("https://explorer.arc.io/tx/%s", txHash)
		memo := fmt.Sprintf("approved savings policy deposited %.2f USDC after a %.2f USDC balance increase", saveAmount, delta)
		receipt := &ExecutionRecord{
			OwnerEmail:       intent.OwnerEmail,
			StandingIntentID: intent.ID,
			FiredAt:          firedAt,
			TxHash:           txHash,
			ArcScanURL:       arcScanURL,
			Memo:             memo,
		}
		if err := w.Store.RecordExecution(ctx, receipt); err != nil {
			log.Printf("[Watcher] Standing intent %s moved funds but receipt persistence failed; intent remains paused", intent.ID)
			continue
		}
		if err := w.Store.CompleteStandingIntentRun(ctx, intent.ID, newKnownBalance, newRunCount, firedAt); err != nil {
			log.Printf("[Watcher] Standing intent %s receipt persisted but re-arm failed; intent remains paused", intent.ID)
			continue
		}

		if intent.NotifyPhone != "" && w.Notifier != nil {
			pctStr := ""
			if intent.Plan.Percentage > 0 {
				pctStr = fmt.Sprintf(" (%.0f%%)", intent.Plan.Percentage)
			}
			msg := fmt.Sprintf(
				"🔒 *Rova Standing Savings Fired! (Go Engine)*\n\n"+
					"• *Incoming Payment*: %.2f USDC\n"+
					"• *Saved to Vault*: %.2f USDC%s\n"+
					"• *Remaining Liquid*: %.2f USDC\n\n"+
					"🔗 *ArcScan Link*:\n%s",
				delta, saveAmount, pctStr, newKnownBalance, arcScanURL,
			)
			if err := w.Notifier.SendMessage(intent.NotifyPhone, msg); err != nil {
				log.Printf("[Watcher] Standing intent %s notification failed", intent.ID)
			}
		}
	}
}

func (w *WatcherEngine) evaluateRules(ctx context.Context) {
	activeRules := w.Store.ListActiveRules()
	if len(activeRules) == 0 {
		return
	}

	for _, rule := range activeRules {
		shopResult := w.Shopper.ShopRates(rule.Pair)
		if !shopResult.Executable {
			log.Printf("[Watcher] Rule %s skipped because only simulated quotes are available", rule.ID)
			continue
		}
		bestRate := shopResult.BestRate

		if rateRuleMatches(rule, bestRate) {
			w.executeMatchedRule(ctx, rule, shopResult)
		}
	}
}

func rateRuleMatches(rule *AgentRule, rate float64) bool {
	return rule.TriggerType == TriggerRateGTE && rate >= rule.TriggerValue ||
		rule.TriggerType == TriggerRateLTE && rate <= rule.TriggerValue
}

func (w *WatcherEngine) executeMatchedRule(ctx context.Context, rule *AgentRule, shopResult *nanopay.QuoteShopResult) {
	if rule.OwnerEmail == "" {
		log.Printf("[Watcher] Rule %s is missing an owner and cannot be claimed", rule.ID)
		return
	}
	if rule.CustodyMode != CustodyManaged {
		log.Printf("[Watcher] Rule %s requires unavailable self-custody approval and remains active", rule.ID)
		return
	}
	if !w.canExecuteAutomatically(rule.Amount) {
		log.Printf("[Watcher] Rule %s matched but automatic-execution policy did not permit it", rule.ID)
		return
	}
	claimedRule, err := w.Store.TryClaimRule(ctx, rule.ID, rule.OwnerEmail)
	if err != nil {
		log.Printf("[Watcher] Rule %s claim failed", rule.ID)
		return
	}
	if claimedRule == nil {
		return
	}
	rule = claimedRule
	if rule.CustodyMode != CustodyManaged || !rateRuleMatches(rule, shopResult.BestRate) || !w.canExecuteAutomatically(rule.Amount) {
		log.Printf("[Watcher] Rule %s no longer satisfies its claimed policy and remains paused", rule.ID)
		return
	}

	txHash, err := w.ChainClient.TransferUSDCFromWallet(ctx, rule.SourceWallet, rule.RecipientIdentifier, rule.Amount)
	if err != nil {
		log.Printf("[Watcher] Rule %s execution failed and remains paused for review", rule.ID)
		return
	}

	operator := ">="
	if rule.TriggerType == TriggerRateLTE {
		operator = "<="
	}
	memo := fmt.Sprintf("approved rate policy executed at %.4f %s target %.4f after %d verified quotes",
		shopResult.BestRate, operator, rule.TriggerValue, shopResult.ProvidersChecked)

	firedAt := time.Now()
	arcScanURL := fmt.Sprintf("https://explorer.arc.io/tx/%s", txHash)
	receipt := &ExecutionRecord{
		OwnerEmail:      rule.OwnerEmail,
		RuleID:          rule.ID,
		FiredAt:         firedAt,
		RateAtExecution: shopResult.BestRate,
		TxHash:          txHash,
		ArcScanURL:      arcScanURL,
		Memo:            memo,
	}
	if err := w.Store.RecordExecution(ctx, receipt); err != nil {
		log.Printf("[Watcher] Rule %s moved funds but receipt persistence failed; rule remains paused", rule.ID)
		return
	}
	if err := w.Store.UpdateRuleStatus(ctx, rule.ID, StatusFired); err != nil {
		log.Printf("[Watcher] Rule %s receipt persisted but final status update failed", rule.ID)
		return
	}

	log.Printf("[Watcher] Rule %s executed and receipt references were persisted", rule.ID)
	if _, err := w.ChainClient.LogExecutionOnchain(ctx, chain.LogExecutionOpts{
		RuleID:          rule.ID,
		SourceWallet:    rule.SourceWallet,
		Recipient:       rule.RecipientIdentifier,
		AmountUsdc:      rule.Amount,
		RateAtExecution: shopResult.BestRate,
		Memo:            memo,
	}); err != nil {
		log.Printf("[Watcher] Rule %s on-chain audit-log write failed", rule.ID)
	}

	if rule.NotifyPhone != "" && w.Notifier != nil {
		if err := w.Notifier.SendExecutionReport(rule.NotifyPhone, NotificationOpts{
			Recipient:        rule.RecipientIdentifier,
			Amount:           rule.Amount,
			Pair:             rule.Pair,
			Rate:             shopResult.BestRate,
			BestProvider:     shopResult.BestProvider,
			ProvidersChecked: shopResult.ProvidersChecked,
			TxHash:           txHash,
			ArcScanURL:       arcScanURL,
			Memo:             memo,
		}); err != nil {
			log.Printf("[Watcher] Rule %s execution notification failed", rule.ID)
		}
	}
}

func (w *WatcherEngine) canExecuteAutomatically(amount float64) bool {
	if w.ChainClient == nil || w.ChainClient.Config == nil || !w.ChainClient.Config.ExecutionEnabled || !w.ChainClient.Config.AutonomousExecutionEnabled {
		return false
	}
	if w.ChainClient.Config.ChainID == config.ArcMainnetChainID {
		return false
	}
	if w.ChainClient.Config.ExecutionMode != "circle" {
		return false
	}
	if amount == 0 {
		return true
	}
	return amount > 0 && amount <= w.ChainClient.Config.MaxAutonomousAmount
}
