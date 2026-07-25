package agent

import (
	"context"
	"fmt"
	"log"
	"time"

	"rova-agent-go/pkg/chain"
	"rova-agent-go/pkg/nanopay"
	"rova-agent-go/pkg/whatsapp"
)

type WatcherEngine struct {
	Store       *Store
	ChainClient *chain.ChainClient
	Shopper     *nanopay.Shopper
	Notifier    *whatsapp.Notifier
	Interval    time.Duration
}

func NewWatcherEngine(store *Store, chainClient *chain.ChainClient, shopper *nanopay.Shopper, notifier *whatsapp.Notifier, interval time.Duration) *WatcherEngine {
	return &WatcherEngine{
		Store:       store,
		ChainClient: chainClient,
		Shopper:     shopper,
		Notifier:    notifier,
		Interval:    interval,
	}
}

func (w *WatcherEngine) StartWatcher(ctx context.Context) {
	ticker := time.NewTicker(w.Interval)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.evaluateRules(ctx)
			}
		}
	}()
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

	if rule.NotifyPhone != "" {
		w.Notifier.SendExecutionReport(rule.NotifyPhone, whatsapp.ReportOpts{
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
