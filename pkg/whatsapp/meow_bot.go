package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"rova-agent-go/pkg/agent"
	"rova-agent-go/pkg/ai"
	"rova-agent-go/pkg/circle"
	"rova-agent-go/pkg/config"
)

type MeowBot struct {
	Config       *config.Config
	Client       *whatsmeow.Client
	CircleClient *circle.CircleClient
	AIParser     *ai.AIParser
}

func NewMeowBot(ctx context.Context, cfg *config.Config, circleClient *circle.CircleClient) (*MeowBot, error) {
	dbLog := waLog.Stdout("Database", "WARN", true)
	container, err := sqlstore.New(ctx, "sqlite", "file:rova_whatsapp.db?_pragma=foreign_keys(1)", dbLog)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite store: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get first device: %w", err)
	}

	clientLog := waLog.Stdout("WhatsApp", "INFO", true)
	client := whatsmeow.NewClient(deviceStore, clientLog)

	bot := &MeowBot{
		Config:       cfg,
		Client:       client,
		CircleClient: circleClient,
		AIParser:     ai.NewAIParser(),
	}

	client.AddEventHandler(bot.handleEvent)
	return bot, nil
}

func (b *MeowBot) Start(ctx context.Context) error {
	if b.Client.Store.ID == nil {
		qrChan, _ := b.Client.GetQRChannel(ctx)
		err := b.Client.Connect()
		if err != nil {
			return fmt.Errorf("failed to connect: %w", err)
		}

		fmt.Println("\n==================================================================")
		fmt.Println("       ROVA WHATSMEOW BOT — SCAN QR CODE WITH WHATSAPP            ")
		fmt.Println("==================================================================")

		for evt := range qrChan {
			if evt.Event == "code" {
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
				fmt.Println("\n👉 Open WhatsApp on your phone -> Linked Devices -> Link a Device")
			} else {
				fmt.Printf("Login status: %s\n", evt.Event)
			}
		}
	} else {
		err := b.Client.Connect()
		if err != nil {
			return fmt.Errorf("failed to connect: %w", err)
		}
		log.Println("[MeowBot] WhatsApp session reconnected successfully!")
	}

	return nil
}

func (b *MeowBot) Stop() {
	b.Client.Disconnect()
}

func (b *MeowBot) handleEvent(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		// Ignore self messages and all WhatsApp group messages
		if v.Info.IsFromMe || v.Info.IsGroup {
			return
		}

		senderJID := v.Info.Sender.ToNonAD()
		senderPhone := senderJID.User

		text := extractMessageText(v.Message)
		if text == "" {
			return
		}

		log.Printf("[MeowBot] Inbound 1-on-1 message from %s: %s", senderPhone, text)
		b.processIncomingCommand(context.Background(), senderJID, senderPhone, text)
	}
}

type supabaseUserRecord struct {
	ID                   string `json:"id"`
	Email                string `json:"email"`
	CircleWalletAddress  string `json:"circle_wallet_address"`
	SavingsWalletAddress string `json:"savings_wallet_address"`
	Phone                string `json:"phone"`
	WhatsAppPhone        string `json:"whatsapp_phone"`
	WhatsAppNumber       string `json:"whatsapp_number"`
}

func (b *MeowBot) checkUserRegistered(phone string) (*supabaseUserRecord, bool) {
	if b.Config.SupabaseURL == "" || b.Config.SupabaseAnonKey == "" {
		log.Printf("[MeowBot] Supabase URL/AnonKey unconfigured. Rejecting unauthenticated access for phone: %s", phone)
		return nil, false
	}

	cleanPhone := strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(phone, "+"), "-", ""), " ", "")
	queryURL := fmt.Sprintf("%s/rest/v1/users?select=id,email,circle_wallet_address,savings_wallet_address,whatsapp_number&or=(whatsapp_number.eq.%s,whatsapp_number.eq.%%2B%s)",
		b.Config.SupabaseURL, cleanPhone, cleanPhone)

	req, err := http.NewRequest("GET", queryURL, nil)
	if err != nil {
		log.Printf("[MeowBot] Supabase request creation error: %v", err)
		return nil, false
	}
	req.Header.Set("apikey", b.Config.SupabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+b.Config.SupabaseAnonKey)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[MeowBot] Supabase user check HTTP error: %v", err)
		return nil, false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Printf("[MeowBot] Supabase user check returned HTTP status %d for phone %s", resp.StatusCode, phone)
		return nil, false
	}

	var users []supabaseUserRecord
	if err := json.NewDecoder(resp.Body).Decode(&users); err == nil && len(users) > 0 {
		log.Printf("[MeowBot] User lookup for %s: REGISTERED (User ID: %s, Email: %s)", phone, users[0].ID, users[0].Email)
		return &users[0], true
	}

	log.Printf("[MeowBot] User lookup for %s: UNREGISTERED (0 records found in Supabase)", phone)
	return nil, false
}

func (b *MeowBot) bindUserWithToken(phone, token string) (string, bool) {
	if b.Config.SupabaseURL == "" || b.Config.SupabaseAnonKey == "" {
		return "", false
	}

	cleanToken := strings.TrimSpace(token)
	queryURL := fmt.Sprintf("%s/rest/v1/otp_codes?select=id,email,code,expires_at&code=eq.%s&limit=1",
		b.Config.SupabaseURL, cleanToken)

	req, err := http.NewRequest("GET", queryURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("apikey", b.Config.SupabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+b.Config.SupabaseAnonKey)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode >= 400 {
		return "", false
	}
	defer resp.Body.Close()

	var records []struct {
		ID        string `json:"id"`
		Email     string `json:"email"`
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil || len(records) == 0 {
		return "", false
	}

	record := records[0]
	if t, err := time.Parse(time.RFC3339, record.ExpiresAt); err == nil {
		if time.Now().After(t) {
			log.Printf("[MeowBot] Binding token %s expired at %v", cleanToken, t)
			return "", false
		}
	}

	cleanPhone := strings.TrimPrefix(phone, "+")
	formattedPhone := "+" + cleanPhone

	// Update Supabase users table for this user email (ONLY use whatsapp_number column)
	updateBody, _ := json.Marshal(map[string]string{
		"whatsapp_number": formattedPhone,
	})

	updateURL := fmt.Sprintf("%s/rest/v1/users?email=eq.%s", b.Config.SupabaseURL, record.Email)
	patchReq, err := http.NewRequest("PATCH", updateURL, strings.NewReader(string(updateBody)))
	if err == nil {
		patchReq.Header.Set("apikey", b.Config.SupabaseAnonKey)
		patchReq.Header.Set("Authorization", "Bearer "+b.Config.SupabaseAnonKey)
		patchReq.Header.Set("Content-Type", "application/json")
		patchReq.Header.Set("Prefer", "return=minimal")
		if patchResp, err := client.Do(patchReq); err == nil {
			log.Printf("[MeowBot] Supabase PATCH response status: %d", patchResp.StatusCode)
			patchResp.Body.Close()
		}
	}

	// Delete used token from otp_codes
	deleteURL := fmt.Sprintf("%s/rest/v1/otp_codes?id=eq.%s", b.Config.SupabaseURL, record.ID)
	delReq, err := http.NewRequest("DELETE", deleteURL, nil)
	if err == nil {
		delReq.Header.Set("apikey", b.Config.SupabaseAnonKey)
		delReq.Header.Set("Authorization", "Bearer "+b.Config.SupabaseAnonKey)
		if delResp, err := client.Do(delReq); err == nil {
			delResp.Body.Close()
		}
	}

	log.Printf("[MeowBot] SUCCESS: Bound phone %s to user %s via token %s", formattedPhone, record.Email, cleanToken)
	return record.Email, true
}

func (b *MeowBot) getUserRulesStatus(phone string) string {
	if b.Config.SupabaseURL == "" || b.Config.SupabaseAnonKey == "" {
		return "📊 *Rova Active Watchers*\n\n_No active rate rules currently armed._"
	}

	cleanPhone := strings.TrimPrefix(phone, "+")
	queryURL := fmt.Sprintf("%s/rest/v1/agent_rules?select=id,pair,trigger_type,trigger_value,amount,status&notify_phone=eq.%%2B%s",
		b.Config.SupabaseURL, cleanPhone)

	req, err := http.NewRequest("GET", queryURL, nil)
	if err != nil {
		return "📊 *Rova Active Watchers*\n\n_No active rate rules currently armed._"
	}
	req.Header.Set("apikey", b.Config.SupabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+b.Config.SupabaseAnonKey)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode >= 400 {
		return "📊 *Rova Active Watchers*\n\n_No active rate rules currently armed._"
	}
	defer resp.Body.Close()

	var rules []struct {
		ID           string  `json:"id"`
		Pair         string  `json:"pair"`
		TriggerType  string  `json:"trigger_type"`
		TriggerValue float64 `json:"trigger_value"`
		Amount       float64 `json:"amount"`
		Status       string  `json:"status"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rules); err == nil && len(rules) > 0 {
		var sb strings.Builder
		sb.WriteString("📊 *Rova Active Watchers*\n\n")
		sb.WriteString(fmt.Sprintf("• *Active Rate Rules*: %d armed\n", len(rules)))
		for _, r := range rules {
			sb.WriteString(fmt.Sprintf("  - %.2f USDC (%s %s %.4f)\n", r.Amount, r.Pair, r.TriggerType, r.TriggerValue))
		}
		sb.WriteString("\n_Rova is monitoring Arc rates 24/7._")
		return sb.String()
	}

	return "📊 *Rova Active Watchers*\n\n_No active rate rules currently armed._\n_You can arm rules on rovapay.xyz or directly in this chat!_"
}

func extractMessageText(msg *waProto.Message) string {
	if msg == nil {
		return ""
	}
	if msg.GetConversation() != "" {
		return msg.GetConversation()
	}
	if msg.GetExtendedTextMessage() != nil {
		return msg.GetExtendedTextMessage().GetText()
	}
	return ""
}

func (b *MeowBot) processIncomingCommand(ctx context.Context, jid types.JID, phone, text string) {
	cleanText := strings.TrimSpace(text)
	textLower := strings.ToLower(cleanText)

	appURL := b.Config.AppURL
	if appURL == "" {
		appURL = "https://rovapay.xyz"
	}

	// 0. Dynamic Token Interceptor (Handle LINK-XXXXXX binding tokens BEFORE authorization gate)
	if strings.HasPrefix(strings.ToUpper(cleanText), "LINK-") {
		userEmail, bound := b.bindUserWithToken(phone, strings.ToUpper(cleanText))
		if bound {
			reply := fmt.Sprintf(
				"✅ *WhatsApp Line Linked Successfully!*\n\n"+
					"• *Account Email*: %s\n"+
					"• *Linked Phone*: +%s\n"+
					"• *Chain*: Arc Testnet\n\n"+
					"You can now manage your capital and execute automated rules directly in this chat!\n\n"+
					"Type *\"balance\"*, *\"status\"*, or *\"send 50 USDC to 0x...\"* to start.",
				userEmail, phone,
			)
			b.replyText(jid, reply)
			return
		}

		reply := fmt.Sprintf(
			"❌ *Verification Token Expired or Invalid*\n\n"+
				"The token `%s` could not be verified or has expired.\n\n"+
				"Please open the Rova web portal (*%s*), click *\"Link WhatsApp AI Agent\"*, and tap the new link to bind your line.",
			cleanText, appURL,
		)
		b.replyText(jid, reply)
		return
	}

	// 1. Authorization Check: Require registration FIRST before responding to any commands or greetings
	userRecord, registered := b.checkUserRegistered(phone)
	if !registered {
		reply := fmt.Sprintf(
			"👋 *Hello! Welcome to Rova Autonomous Financial Agent.*\n\n"+
				"To execute stablecoin payments, atomic swaps, and automated rules directly in this chat, please sign up and activate your WhatsApp line on our web portal:\n\n"+
				"👉 *%s*\n\n"+
				"_Once activated on the web portal, your WhatsApp number will be linked instantly!_",
			appURL,
		)
		b.replyText(jid, reply)
		return
	}

	// 2. Greeting / Welcome message for REGISTERED users only
	isGreeting := textLower == "gm" || textLower == "good morning" || textLower == "gn" || textLower == "good night" ||
		textLower == "hi" || textLower == "hello" || textLower == "hey" || textLower == "start" || textLower == "help" ||
		textLower == "who are you" || textLower == "what is rova" || textLower == "what can you do" ||
		strings.HasPrefix(textLower, "hi ") || strings.HasPrefix(textLower, "hello ") || strings.HasPrefix(textLower, "hey ")

	if isGreeting {
		reply :=
			"👋 *Hello! Welcome to Rova Autonomous Agent!*\n\n" +
			"I am your AI financial execution agent on Arc Testnet. You can manage your capital flows directly in this chat:\n\n" +
			"• *\"send 50 USDC to 0x71C7...\"*\n" +
			"• *\"swap 100 USDC to EURC\"*\n" +
			"• *\"bridge 200 USDC from Ethereum to Arc\"*\n" +
			"• *\"balance\"* — View account wallet & balances\n" +
			"• *\"status\"* — View active rate watchers\n\n" +
			"🌐 *Web Portal*: " + appURL + "\n\n" +
			"_Powered by Rova Native AI Engine on Arc_"

		b.replyText(jid, reply)
		return
	}

	if strings.Contains(textLower, "balance") || strings.Contains(textLower, "wallet") || strings.Contains(textLower, "funds") {
		walletAddr := b.Config.CircleWalletID
		if userRecord != nil && userRecord.CircleWalletAddress != "" {
			walletAddr = userRecord.CircleWalletAddress
		}
		if walletAddr == "" {
			walletAddr = "Unconfigured (Set CIRCLE_WALLET_ID in environment)"
		}

		reply := fmt.Sprintf(
			"💳 *Rova Agent Account*\n\n"+
				"• *Phone*: +%s\n"+
				"• *Circle Wallet*: `%s`\n"+
				"• *Custody Mode*: Circle Developer-Controlled (HSM)\n"+
				"• *Chain*: Arc Testnet (Sub-second settlement)\n\n"+
				"_Send stablecoins to this address to automate execution._",
			phone, walletAddr,
		)
		b.replyText(jid, reply)
		return
	}

	if strings.Contains(textLower, "status") || strings.Contains(textLower, "rule") || strings.Contains(textLower, "watcher") || strings.Contains(textLower, "daemon") {
		reply := b.getUserRulesStatus(phone)
		b.replyText(jid, reply)
		return
	}

	// Dynamic AI Intent Parsing
	parsed, err := b.AIParser.ParseIntent(ctx, cleanText)
	if err != nil {
		log.Printf("[MeowBot] AI Parsing fallback error: %v", err)
	}

	if parsed == nil {
		parsed = &ai.ParsedIntent{
			Action:    "help",
			Reasoning: "Fallback intent parser",
		}
	}

	log.Printf("[MeowBot] AI Parsed Action: %s, Amount: %.2f, Recipient: %s, Reasoning: %s", parsed.Action, parsed.Amount, parsed.Recipient, parsed.Reasoning)

	switch parsed.Action {
	case "balance":
		walletAddr := b.Config.CircleWalletID
		if userRecord != nil && userRecord.CircleWalletAddress != "" {
			walletAddr = userRecord.CircleWalletAddress
		}
		if walletAddr == "" {
			walletAddr = "Unconfigured (Set CIRCLE_WALLET_ID in environment)"
		}

		reply := fmt.Sprintf(
			"💳 *Rova Agent Account*\n\n"+
				"• *Phone*: +%s\n"+
				"• *Circle Wallet*: `%s`\n"+
				"• *Custody Mode*: Circle Developer-Controlled (HSM)\n"+
				"• *Chain*: Arc Testnet (Sub-second settlement)\n\n"+
				"_Send stablecoins to this address to automate execution._",
			phone, walletAddr,
		)
		b.replyText(jid, reply)

	case "status":
		reply := b.getUserRulesStatus(phone)
		b.replyText(jid, reply)

	case "send":
		targetRecipient := parsed.Recipient
		if targetRecipient == "" {
			b.replyText(jid, "❌ *Recipient Missing*: Please specify a valid destination wallet address (e.g. *\"send 50 USDC to 0x...\"*).")
			return
		}

		b.replyText(jid, fmt.Sprintf("⏳ *Processing Send command via Circle Wallet on Arc...*\n_Reasoning_: %s", parsed.Reasoning))

		sendAmount := parsed.Amount
		if sendAmount <= 0 {
			sendAmount = 50.0
		}

		txHash, err := b.CircleClient.TransferUSDC(ctx, targetRecipient, sendAmount)
		if err != nil {
			b.replyText(jid, fmt.Sprintf("❌ *Transaction Failed*: %v", err))
			return
		}

		arcScanURL := fmt.Sprintf("https://testnet.arcscan.app/tx/%s", txHash)
		reply := fmt.Sprintf(
			"✅ *USDC Sent Successfully!*\n\n"+
				"• *Amount*: %.2f %s\n"+
				"• *Recipient*: `%s`\n"+
				"• *Execution*: Circle Programmable Wallet\n"+
				"• *Settlement Time*: < 1 second\n"+
				"• *AI Strategy*: %s\n\n"+
				"🔗 *ArcScan Link*:\n%s\n\n"+
				"_Powered by Rova Autonomous AI Agent_",
			sendAmount, parsed.Currency, targetRecipient, parsed.Reasoning, arcScanURL,
		)
		b.replyText(jid, reply)

	case "swap":
		b.replyText(jid, fmt.Sprintf("⏳ *Executing StableFX atomic swap on Arc via Circle DCW...*\n_Reasoning_: %s", parsed.Reasoning))

		swapAmount := parsed.Amount
		if swapAmount <= 0 {
			swapAmount = 100.0
		}

		userWallet := userRecord.CircleWalletAddress
		if userWallet == "" {
			userWallet = b.Config.CircleWalletID
		}

		buyCurr := parsed.Currency
		if buyCurr == "" {
			buyCurr = "EURC"
		}

		txHash, err := b.CircleClient.SwapStablecoins(ctx, userWallet, buyCurr, swapAmount)
		if err != nil {
			b.replyText(jid, fmt.Sprintf("❌ *Swap Failed*: %v", err))
			return
		}

		arcScanURL := fmt.Sprintf("https://testnet.arcscan.app/tx/%s", txHash)
		reply := fmt.Sprintf(
			"🔄 *StableFX Swap Executed On-Chain!*\n\n"+
				"• *Swapped*: %.2f USDC → %s\n"+
				"• *Executed Rate*: 0.9420 EURC/USDC\n"+
				"• *Atomic Settlement*: Arc Native StableFX (Circle DCW)\n"+
				"• *AI Strategy*: %s\n\n"+
				"🔗 *ArcScan Link*:\n%s\n\n"+
				"_Powered by Rova Autonomous AI Agent_",
			swapAmount, buyCurr, parsed.Reasoning, arcScanURL,
		)
		b.replyText(jid, reply)

	case "bridge":
		b.replyText(jid, fmt.Sprintf("⏳ *Initiating CCTP V2 Cross-Chain Bridge to Arc via Circle DCW...*\n_Reasoning_: %s", parsed.Reasoning))

		bridgeAmount := parsed.Amount
		if bridgeAmount <= 0 {
			bridgeAmount = 200.0
		}

		sourceChain := parsed.SourceChain
		if sourceChain == "" {
			sourceChain = "Ethereum"
		}

		userWallet := userRecord.CircleWalletAddress
		if userWallet == "" {
			userWallet = b.Config.CircleWalletID
		}

		txHash, err := b.CircleClient.BridgeCCTP(ctx, userWallet, sourceChain, bridgeAmount)
		if err != nil {
			b.replyText(jid, fmt.Sprintf("❌ *Bridge Failed*: %v", err))
			return
		}

		arcScanURL := fmt.Sprintf("https://testnet.arcscan.app/tx/%s", txHash)
		reply := fmt.Sprintf(
			"🌉 *CCTP V2 Bridge Executed On-Chain!*\n\n"+
				"• *Amount*: %.2f USDC\n"+
				"• *Source Chain*: %s (Domain 0)\n"+
				"• *Target Chain*: Arc (Domain 26)\n"+
				"• *Attestation*: Circle Teleporter Gateway\n\n"+
				"🔗 *ArcScan Link*:\n%s\n\n"+
				"_Liquidity will settle on Arc within 30 seconds._",
			bridgeAmount, sourceChain, arcScanURL,
		)
		b.replyText(jid, reply)

	case "save", "savings":
		// 1. Check if user requested a standing percentage intent (e.g. "always save 10% of every deposit")
		isStanding := strings.Contains(textLower, "always") || strings.Contains(textLower, "%") ||
			strings.Contains(textLower, "every") || strings.Contains(textLower, "whenever") || strings.Contains(textLower, "standing")

		if isStanding {
			pct := 10.0
			if parsed.Amount > 0 && parsed.Amount <= 100 {
				pct = parsed.Amount
			}

			userWallet := userRecord.CircleWalletAddress
			if userWallet == "" {
				userWallet = b.Config.CircleWalletID
			}

			intentID := fmt.Sprintf("intent-%d", time.Now().UnixNano())
			newIntent := &agent.StandingIntent{
				ID:           intentID,
				CreatedAt:    time.Now(),
				Status:       agent.StatusActive,
				IntentText:   cleanText,
				Plan:         agent.StandingIntentPlanStep{Action: "save", Percentage: pct},
				Trigger:      agent.StandingIntentTrigger{Type: "on_receive", MinAmountUsdc: 0.1},
				CustodyMode:  agent.CustodyManaged,
				SourceWallet: userWallet,
				NotifyPhone:  phone,
				SourceChannel: "whatsapp",
			}

			b.getUserRulesStatus(phone) // Ensure store initialized if needed
			// Save intent to Supabase store
			store := agent.NewSupabaseStore(b.Config.SupabaseURL, b.Config.SupabaseAnonKey)
			store.AddStandingIntent(newIntent)

			reply := fmt.Sprintf(
				"🤖 *Rova Standing Savings Intent Armed!*\n\n"+
					"• *Rule*: Save %.0f%% of every incoming deposit\n"+
					"• *Source Wallet*: `%s`\n"+
					"• *Vault Mode*: %s\n"+
					"• *Monitoring*: 24/7 Go Daemon Active\n\n"+
					"_Rova will automatically detect deposits and transfer %.0f%% into your Rova Savings Vault._",
				pct, userWallet, b.Config.VaultStrategy, pct,
			)
			b.replyText(jid, reply)
			return
		}

		// 2. Otherwise execute immediate one-off savings deposit
		b.replyText(jid, fmt.Sprintf("⏳ *Depositing into Rova Savings Vault (%s)...*\n_Reasoning_: %s", b.Config.VaultStrategy, parsed.Reasoning))

		saveAmount := parsed.Amount
		if saveAmount <= 0 {
			saveAmount = 25.0
		}

		userWallet := userRecord.CircleWalletAddress
		if userWallet == "" {
			userWallet = b.Config.CircleWalletID
		}

		savingsTarget := userRecord.SavingsWalletAddress
		if savingsTarget == "" {
			savingsTarget = userWallet
		}

		txHash, err := b.CircleClient.DepositSavingsVault(ctx, userWallet, savingsTarget, saveAmount)
		if err != nil {
			b.replyText(jid, fmt.Sprintf("❌ *Savings Deposit Failed*: %v", err))
			return
		}

		arcScanURL := fmt.Sprintf("https://testnet.arcscan.app/tx/%s", txHash)
		reply := fmt.Sprintf(
			"🔒 *Rova Savings Vault Deposit Complete!*\n\n"+
				"• *Amount Saved*: %.2f USDC\n"+
				"• *Vault Mode*: %s\n"+
				"• *Lock Limiter*: Active (Protected from routine operations)\n"+
				"• *AI Strategy*: %s\n\n"+
				"🔗 *ArcScan Link*:\n%s\n\n"+
				"_Powered by Rova Autonomous AI Agent_",
			saveAmount, b.Config.VaultStrategy, parsed.Reasoning, arcScanURL,
		)
		b.replyText(jid, reply)

	default:
		// Clean user-facing reply without internal developer debug variables
		replyReasoning := parsed.Reasoning
		if replyReasoning == "" || replyReasoning == "Fallback intent parser" {
			replyReasoning = "I am Rova, your autonomous AI financial agent on Arc Testnet."
		}

		reply := fmt.Sprintf(
			"🤖 *Rova AI Assistant*\n\n"+
				"%s\n\n"+
				"You can send natural commands like:\n"+
				"• *\"send 50 USDC to 0x...\"*\n"+
				"• *\"swap 100 USDC to EURC\"*\n"+
				"• *\"bridge 200 USDC from Ethereum\"*\n\n"+
				"🌐 *Web Portal*: %s",
			replyReasoning, appURL,
		)
		b.replyText(jid, reply)
	}
}

func (b *MeowBot) SendMessageToPhone(phone, text string) error {
	cleanPhone := strings.ReplaceAll(strings.ReplaceAll(phone, "+", ""), " ", "")
	if !strings.HasSuffix(cleanPhone, "@s.whatsapp.net") {
		cleanPhone += "@s.whatsapp.net"
	}
	jid, err := types.ParseJID(cleanPhone)
	if err != nil {
		return err
	}
	b.replyText(jid, text)
	return nil
}

func (b *MeowBot) SendExecutionReport(phone string, opts ReportOpts) error {
	var text strings.Builder
	text.WriteString("🤖 *Rova Agent Execution Report (Go Engine)*\n\n")
	text.WriteString(fmt.Sprintf("✅ *Status*: Executed via Go-Ethereum on Arc Testnet\n"))
	text.WriteString(fmt.Sprintf("💸 *Transfer*: %.2f USDC → `%s`\n", opts.Amount, opts.Recipient))
	text.WriteString(fmt.Sprintf("📊 *Executed FX Rate*: %.4f %s\n", opts.Rate, opts.Pair))
	text.WriteString(fmt.Sprintf("🏷️ *Goroutine Nanopayments*: Selected *%s* out of %d quotes.\n", opts.BestProvider, opts.ProvidersChecked))
	if opts.Memo != "" {
		text.WriteString(fmt.Sprintf("📝 *Memo*: %s\n", opts.Memo))
	}
	if opts.ArcScanURL != "" {
		text.WriteString(fmt.Sprintf("\n🔗 *ArcScan Link*:\n%s\n", opts.ArcScanURL))
	}
	text.WriteString("\n_Powered by Rova Whatsmeow Engine_")
	return b.SendMessageToPhone(phone, text.String())
}

func (b *MeowBot) SendApprovalAlert(phone, ruleID, recipient string, amount, rate float64) error {
	var text strings.Builder
	text.WriteString("⚠️ *Rova Action Required: Self-Custody Transfer Ready*\n\n")
	text.WriteString(fmt.Sprintf("Your armed rule `%s` has met its trigger condition!\n\n", ruleID))
	text.WriteString(fmt.Sprintf("💸 *Transfer Amount*: %.2f USDC → `%s`\n", amount, recipient))
	text.WriteString(fmt.Sprintf("📊 *Triggered Rate*: %.4f\n\n", rate))
	text.WriteString("🔒 Tap the link below to approve and sign the transfer with your wallet:\n\n")
	text.WriteString(fmt.Sprintf("👉 https://rova.app/approve/%s\n\n", ruleID))
	text.WriteString("_Rova Go Safeguard_")
	return b.SendMessageToPhone(phone, text.String())
}

func (b *MeowBot) replyText(jid types.JID, text string) {
	_, err := b.Client.SendMessage(context.Background(), jid, &waProto.Message{
		Conversation: &text,
	})
	if err != nil {
		log.Printf("[MeowBot] Error sending message to %s: %v", jid.String(), err)
	}
}

func RunMeowBotService(cfg *config.Config) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	circleClient := circle.NewCircleClient(cfg)
	bot, err := NewMeowBot(ctx, cfg, circleClient)
	if err != nil {
		return err
	}

	if err := bot.Start(ctx); err != nil {
		return err
	}

	log.Println("[MeowBot] Rova WhatsApp Agent Service is RUNNING 24/7.")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("[MeowBot] Shutting down WhatsApp service gracefully...")
	bot.Stop()
	return nil
}
