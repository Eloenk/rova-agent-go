package whatsapp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	Config *config.Config
}

func NewNotifier(cfg *config.Config) *Notifier {
	return &Notifier{Config: cfg}
}

func (n *Notifier) SendMessage(toPhone, text string) error {
	if n.Config.MockMode || n.Config.WhatsAppAPIToken == "" {
		log.Printf("[WhatsApp Go Mock Outbound] to=%s:\n%s", toPhone, text)
		return nil
	}

	cleanPhone := strings.ReplaceAll(toPhone, "+", "")
	url := fmt.Sprintf("https://graph.facebook.com/v18.0/%s/messages", n.Config.WhatsAppPhoneNumberID)

	payload := map[string]interface{}{
		"messaging_product": "whatsapp",
		"recipient_type":     "individual",
		"to":                cleanPhone,
		"type":              "text",
		"text":              map[string]string{"body": text},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+n.Config.WhatsAppAPIToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("whatsapp API returned status %d", resp.StatusCode)
	}

	return nil
}

func (n *Notifier) SendExecutionReport(toPhone string, opts ReportOpts) error {
	var text strings.Builder
	text.WriteString("🤖 *Rova Agent Execution Report (Go Engine)*\n\n")
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

	text.WriteString("\n_Powered by Rova Go Autonomous Agentic Engine_")

	return n.SendMessage(toPhone, text.String())
}

func (n *Notifier) SendApprovalAlert(toPhone, ruleID, recipient string, amount, rate float64) error {
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
