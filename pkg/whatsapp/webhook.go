package whatsapp

import (
	"fmt"
	"log"
	"strings"

	"rova-agent-go/pkg/config"
)

type ReportOpts struct {
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

type Notifier struct {
	Config  *config.Config
	MeowBot *MeowBot
}

func NewNotifier(cfg *config.Config) *Notifier {
	return &Notifier{Config: cfg}
}

func NewNotifierWithBot(cfg *config.Config, bot *MeowBot) *Notifier {
	return &Notifier{Config: cfg, MeowBot: bot}
}

func (n *Notifier) SendMessage(toPhone, text string) error {
	if n.MeowBot != nil {
		return n.MeowBot.SendMessageToPhone(toPhone, text)
	}
	log.Printf("[Whatsmeow Outbound Log] to=%s:\n%s", toPhone, text)
	return nil
}

func (n *Notifier) SendExecutionReport(toPhone string, opts ReportOpts) error {
	if n.MeowBot != nil {
		return n.MeowBot.SendExecutionReport(toPhone, opts)
	}
	var text strings.Builder
	text.WriteString("🤖 *Rova Agent Execution Report (Whatsmeow Engine)*\n\n")
	text.WriteString(fmt.Sprintf("✅ *Status*: Executed via Go-Ethereum on Arc Testnet\n"))
	text.WriteString(fmt.Sprintf("💸 *Transfer*: %.2f USDC → `%s`\n", opts.Amount, opts.Recipient))
	text.WriteString(fmt.Sprintf("📊 *Executed FX Rate*: %.4f %s\n", opts.Rate, opts.Pair))
	text.WriteString(fmt.Sprintf("🏷️ *Goroutine Nanopayments*: Selected *%s* out of %d quotes via x402.\n", opts.BestProvider, opts.ProvidersChecked))

	if opts.Memo != "" {
		text.WriteString(fmt.Sprintf("📝 *Memo*: %s\n", opts.Memo))
	}

	if opts.ArcScanURL != "" {
		text.WriteString(fmt.Sprintf("\n🔗 *ArcScan Link*:\n%s\n", opts.ArcScanURL))
	}

	text.WriteString("\n_Powered by Rova Whatsmeow Engine_")

	return n.SendMessage(toPhone, text.String())
}

func (n *Notifier) SendApprovalAlert(toPhone, ruleID, recipient string, amount, rate float64) error {
	if n.MeowBot != nil {
		return n.MeowBot.SendApprovalAlert(toPhone, ruleID, recipient, amount, rate)
	}
	var text strings.Builder
	text.WriteString("⚠️ *Rova Action Required: Self-Custody Transfer Ready*\n\n")
	text.WriteString(fmt.Sprintf("Your armed rule `%s` has met its trigger condition!\n\n", ruleID))
	text.WriteString(fmt.Sprintf("💸 *Transfer Amount*: %.2f USDC → `%s`\n", amount, recipient))
	text.WriteString(fmt.Sprintf("📊 *Triggered Rate*: %.4f\n\n", rate))
	text.WriteString("🔒 Tap the link below to approve and sign the transfer with your wallet:\n\n")
	text.WriteString(fmt.Sprintf("👉 https://rova.app/approve/%s\n\n", ruleID))
	text.WriteString("_Rova Go Safeguard_")

	return n.SendMessage(toPhone, text.String())
}

