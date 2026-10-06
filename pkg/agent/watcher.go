package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"rova-agent-go/pkg/chain"
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

	// Mode 0: Initialize Supabase Realtime WebSocket Push Subscription for Rules & Intents
	if w.Store != nil {
		log.Println("[Watcher Engine] Initializing Supabase Realtime WebSocket (wss://) Subscription...")
		w.Store.StartRealtimeSubscription(ctx)
	}

	// Mode 1: Real-time WSS Event Subscription
	if w.ChainClient != nil && w.ChainClient.Config != nil && w.ChainClient.Config.BalanceMode == "wss" {
		log.Println("[Watcher Engine] Initializing Real-Time WSS Transfer Event Listener for Active Rule Wallets...")
		w.ChainClient.ListenUSDCTransferEvents(ctx, w.getActiveTargetWallets, func(toAddress string, amount float64, txHash string) {
			w.handleWSSTransferEvent(ctx, toAddress, amount, txHash)
		})
	} else {
		log.Println("[Watcher Engine] Running in Multi-RPC Failover Polling Mode")
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.evaluateRules(ctx)
				w.evaluateStandingIntents(ctx)
			}
		}
	}()
}

func (w *WatcherEngine) handleWSSTransferEvent(ctx context.Context, toAddress string, amount float64, transferTxHash string) {
	intents := w.Store.ListActiveStandingIntents()
	for _, intent := range intents {
		if strings.EqualFold(intent.SourceWallet, toAddress) {
			if intent.CustodyMode != CustodyManaged || !w.canExecuteAutomatically(0) {
				log.Printf("[WSS Event Trigger] Standing Intent %s matched but automatic execution is disabled or not managed custody", intent.ID)
				continue
			}

			var saveAmount float64
			if intent.Plan.Percentage > 0 {
				saveAmount = amount * (intent.Plan.Percentage / 100.0)
			} else if intent.Plan.Amount > 0 {
				saveAmount = intent.Plan.Amount
			} else {
				saveAmount = amount * 0.10 // Default 10%
			}

			if saveAmount <= 0 {
				continue
			}
			if !w.canExecuteAutomatically(saveAmount) {
				log.Printf("[WSS Event Trigger] Standing Intent %s exceeded automatic-execution policy", intent.ID)
				continue
			}

			log.Printf("[WSS Event Trigger] Standing Intent %s matched! Transfer: %.2f USDC to %s, Vault Save: %.2f USDC", intent.ID, amount, toAddress, saveAmount)

			var txHash string
			var err error
			if w.ChainClient.CircleClient != nil {
				txHash, err = w.ChainClient.CircleClient.DepositSavingsVault(ctx, intent.SourceWallet, intent.SourceWallet, saveAmount)
			} else {
				txHash, err = w.ChainClient.TransferUSDC(ctx, intent.SourceWallet, saveAmount)
			}

			if err != nil {
				log.Printf("[WSS Event Trigger] Savings Vault deposit failed: %v", err)
				continue
			}

			newRunCount := intent.RunCount + 1
			w.Store.UpdateStandingIntentState(intent.ID, intent.LastKnownBalance, newRunCount, time.Now())

			arcScanURL := fmt.Sprintf("https://testnet.arcscan.app/tx/%s", txHash)
			w.Store.RecordExecution(&ExecutionRecord{
				ID:         fmt.Sprintf("exec-wss-%d", time.Now().UnixNano()),
				RuleID:     intent.ID,
				FiredAt:    time.Now(),
				TxHash:     txHash,
				ArcScanURL: arcScanURL,
				Memo:       fmt.Sprintf("wss-event-exec: 10%% savings deposit for incoming %.2f USDC (Ref Tx: %s)", amount, transferTxHash),
			})

			if intent.NotifyPhone != "" && w.Notifier != nil {
				pctStr := ""
				if intent.Plan.Percentage > 0 {
					pctStr = fmt.Sprintf(" (%.0f%%)", intent.Plan.Percentage)
				}
				msg := fmt.Sprintf(
					"⚡ *Rova Real-Time WSS Savings Fired! (Go Engine)*\n\n"+
						"• *Incoming Payment*: %.2f USDC\n"+
						"• *Saved to Vault*: %.2f USDC%s\n"+
						"• *Deposit Tx*: `%s`\n\n"+
						"🔗 *ArcScan Link*:\n%s\n\n"+
						"_Rova Real-Time Event Listener_",
					amount, saveAmount, pctStr, txHash, arcScanURL,
				)
				w.Notifier.SendMessage(intent.NotifyPhone, msg)
			}
		}
	}
}

func (w *WatcherEngine) evaluateStandingIntents(ctx context.Context) {
	intents := w.Store.ListActiveStandingIntents()
	if len(intents) == 0 {
		return
	}

	for _, intent := range intents {
		if intent.SourceWallet == "" {
			continue
		}
		if intent.CustodyMode != CustodyManaged || !w.canExecuteAutomatically(0) {
			continue
		}

		balance, err := w.ChainClient.GetBalanceUSDCWithFailover(ctx, intent.SourceWallet)
		if err != nil {
			continue
		}

		if intent.LastKnownBalance <= 0 {
			// Set initial baseline
			w.Store.UpdateStandingIntentState(intent.ID, balance, intent.RunCount, time.Now())
			continue
		}

		delta := balance - intent.LastKnownBalance
		minAmount := intent.Trigger.MinAmountUsdc
		if minAmount <= 0 {
			minAmount = 0.01
		}

		if delta >= minAmount {
			// Calculate savings amount based on plan
			var saveAmount float64
			if intent.Plan.Percentage > 0 {
				saveAmount = delta * (intent.Plan.Percentage / 100.0)
			} else if intent.Plan.Amount > 0 {
				saveAmount = intent.Plan.Amount
			} else {
				saveAmount = delta * 0.10 // Default 10%
			}

			if saveAmount <= 0 {
				continue
			}
			if !w.canExecuteAutomatically(saveAmount) {
				log.Printf("[Watcher] Standing Intent %s exceeded automatic-execution policy", intent.ID)
				continue
			}

			log.Printf("[Watcher] Standing Intent %s fired! Incoming Delta: %.2f USDC, Savings: %.2f USDC", intent.ID, delta, saveAmount)

			var txHash string
			if w.ChainClient.CircleClient != nil {
				txHash, err = w.ChainClient.CircleClient.DepositSavingsVault(ctx, intent.SourceWallet, intent.SourceWallet, saveAmount)
			} else {
				txHash, err = w.ChainClient.TransferUSDC(ctx, intent.SourceWallet, saveAmount)
			}

			if err != nil {
				log.Printf("[Watcher] Standing intent vault deposit error for %s: %v", intent.ID, err)
				continue
			}

			newRunCount := intent.RunCount + 1
			newKnownBalance := balance - saveAmount
			w.Store.UpdateStandingIntentState(intent.ID, newKnownBalance, newRunCount, time.Now())

			arcScanURL := fmt.Sprintf("https://testnet.arcscan.app/tx/%s", txHash)
			w.Store.RecordExecution(&ExecutionRecord{
				ID:         fmt.Sprintf("exec-intent-%d", time.Now().UnixNano()),
				RuleID:     intent.ID,
				FiredAt:    time.Now(),
				TxHash:     txHash,
				ArcScanURL: arcScanURL,
				Memo:       fmt.Sprintf("auto-exec: 10%% savings vault deposit for incoming %.2f USDC", delta),
			})

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
						"🔗 *ArcScan Link*:\n%s\n\n"+
						"_Rova 24/7 Autonomous Saver_",
					delta, saveAmount, pctStr, newKnownBalance, arcScanURL,
				)
				w.Notifier.SendMessage(intent.NotifyPhone, msg)
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
		bestRate := shopResult.BestRate

		matched := false
		if rule.TriggerType == TriggerRateGTE && bestRate >= rule.TriggerValue {
			matched = true
		} else if rule.TriggerType == TriggerRateLTE && bestRate <= rule.TriggerValue {
			matched = true
		}

		if matched {
			w.executeMatchedRule(ctx, rule, shopResult)
		}
	}
}

func (w *WatcherEngine) executeMatchedRule(ctx context.Context, rule *AgentRule, shopResult *nanopay.QuoteShopResult) {
	if rule.CustodyMode == CustodySelfCustody {
		w.Store.UpdateRuleStatus(rule.ID, StatusReadyToExecute)
		if rule.NotifyPhone != "" {
			w.Notifier.SendApprovalAlert(rule.NotifyPhone, rule.ID, rule.RecipientIdentifier, rule.Amount, shopResult.BestRate)
		}
		return
	}
	if !w.canExecuteAutomatically(rule.Amount) {
		w.Store.UpdateRuleStatus(rule.ID, StatusReadyToExecute)
		log.Printf("[Watcher] Rule %s matched but automatic-execution policy did not permit it", rule.ID)
		return
	}

	txHash, err := w.ChainClient.TransferUSDC(ctx, rule.RecipientIdentifier, rule.Amount)
	if err != nil {
		log.Printf("[Watcher] USDC Transfer error for rule %s: %v", rule.ID, err)
		return
	}

	memo := fmt.Sprintf("auto-exec: rate %.4f >= target %.4f · shopped %d quotes (%s @ %.4f)",
		shopResult.BestRate, rule.TriggerValue, shopResult.ProvidersChecked, shopResult.BestProvider, shopResult.BestRate)

	onchainTxHash, err := w.ChainClient.LogExecutionOnchain(ctx, chain.LogExecutionOpts{
		RuleID:          rule.ID,
		Recipient:       rule.RecipientIdentifier,
		AmountUsdc:      rule.Amount,
		RateAtExecution: shopResult.BestRate,
		Memo:            memo,
	})
	if err != nil {
		log.Printf("[Watcher] Onchain execution log error: %v", err)
	}

	w.Store.UpdateRuleStatus(rule.ID, StatusFired)
	arcScanURL := fmt.Sprintf("https://testnet.arcscan.app/tx/%s", txHash)

	w.Store.RecordExecution(&ExecutionRecord{
		ID:              fmt.Sprintf("exec-%d", time.Now().UnixNano()),
		RuleID:          rule.ID,
		FiredAt:         time.Now(),
		RateAtExecution: shopResult.BestRate,
		TxHash:          txHash,
		ArcScanURL:      arcScanURL,
		Memo:            memo,
	})

	log.Printf("[Watcher] Rule %s FIRED: %s, OnchainLog: %s", rule.ID, txHash, onchainTxHash)

	if rule.NotifyPhone != "" && w.Notifier != nil {
		w.Notifier.SendExecutionReport(rule.NotifyPhone, NotificationOpts{
			Recipient:        rule.RecipientIdentifier,
			Amount:           rule.Amount,
			Pair:             rule.Pair,
			Rate:             shopResult.BestRate,
			BestProvider:     shopResult.BestProvider,
			ProvidersChecked: shopResult.ProvidersChecked,
			TxHash:           txHash,
			ArcScanURL:       arcScanURL,
			Memo:             memo,
		})
	}
}

func (w *WatcherEngine) canExecuteAutomatically(amount float64) bool {
	if w.ChainClient == nil || w.ChainClient.Config == nil || !w.ChainClient.Config.ExecutionEnabled {
		return false
	}
	if amount == 0 {
		return true
	}
	return amount > 0 && amount <= w.ChainClient.Config.MaxAutonomousAmount
}
