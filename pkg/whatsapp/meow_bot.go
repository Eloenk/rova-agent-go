package whatsapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
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
	"rova-agent-go/pkg/chain"
	"rova-agent-go/pkg/circle"
	"rova-agent-go/pkg/config"
	"rova-agent-go/pkg/nanopay"
)

type MeowBot struct {
	Config          *config.Config
	Client          *whatsmeow.Client
	CircleClient    *circle.CircleClient
	AIParser        *ai.AIParser
	pendingActions  map[string]pendingAction
	pendingActionMu sync.Mutex
}

type pendingAction struct {
	ConfirmationCode string
	Action           string
	WalletAddress    string
	Recipient        string
	BuyCurrency      string
	DestinationChain string
	Amount           float64
	DepositID        int64
	ExpiresAt        time.Time
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
		Config:         cfg,
		Client:         client,
		CircleClient:   circleClient,
		AIParser:       ai.NewAIParser(),
		pendingActions: make(map[string]pendingAction),
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

		// WhatsApp LID Resolution: When sender JID is @lid (Linked ID),
		// the .User field contains a meaningless opaque number, NOT the phone number.
		// Resolve it to the actual phone-based JID via whatsmeow's LID→PN store.
		if senderJID.Server == "lid" {
			pnJID, err := b.Client.Store.LIDs.GetPNForLID(context.Background(), senderJID)
			if err == nil && !pnJID.IsEmpty() {
				log.Printf("[MeowBot] Resolved LID %s → phone JID %s", senderJID.String(), pnJID.String())
				senderPhone = pnJID.User
				senderJID = pnJID // Use phone-based JID for replies too
			} else {
				log.Printf("[MeowBot] WARNING: Could not resolve LID %s to phone number (err: %v). User lookup may fail.", senderJID.String(), err)
			}
		}

		text := extractMessageText(v.Message)
		if text == "" {
			return
		}

		log.Printf("[MeowBot] Inbound 1-on-1 message from %s (%d characters)", senderPhone, len(text))
		go b.processIncomingCommand(context.Background(), senderJID, senderPhone, text)
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
	if b.Config.SupabaseURL == "" || b.Config.SupabaseServiceRoleKey == "" {
		log.Printf("[MeowBot] Supabase service credentials are unconfigured. Rejecting access for phone: %s", phone)
		return nil, false
	}

	cleanPhone := strings.ReplaceAll(strings.ReplaceAll(strings.TrimLeft(phone, "+"), "-", ""), " ", "")
	queryURL := fmt.Sprintf("%s/rest/v1/users?select=id,email,circle_wallet_address,savings_wallet_address,whatsapp_number&or=(whatsapp_number.eq.%s,whatsapp_number.eq.%%2B%s)",
		b.Config.SupabaseURL, cleanPhone, cleanPhone)

	req, err := http.NewRequest("GET", queryURL, nil)
	if err != nil {
		log.Printf("[MeowBot] Supabase request creation error: %v", err)
		return nil, false
	}
	req.Header.Set("apikey", b.Config.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+b.Config.SupabaseServiceRoleKey)

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

func (b *MeowBot) resolveRecipientAddress(recipient string) (string, error) {
	clean := strings.TrimSpace(recipient)
	if clean == "" {
		return "", fmt.Errorf("recipient identifier is empty")
	}

	if strings.HasPrefix(clean, "0x") && len(clean) == 42 {
		return clean, nil
	}

	if b.Config.SupabaseURL == "" || b.Config.SupabaseServiceRoleKey == "" {
		return "", fmt.Errorf("database unconfigured; unable to resolve recipient %s", clean)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	var queryURL string

	if strings.Contains(clean, "@") {
		cleanEmail := strings.ToLower(clean)
		queryURL = fmt.Sprintf("%s/rest/v1/users?select=circle_wallet_address&email=eq.%s&limit=1",
			b.Config.SupabaseURL, cleanEmail)
	} else {
		cleanPhone := strings.ReplaceAll(strings.ReplaceAll(strings.TrimLeft(clean, "+"), "-", ""), " ", "")
		queryURL = fmt.Sprintf("%s/rest/v1/users?select=circle_wallet_address&or=(whatsapp_number.eq.%s,whatsapp_number.eq.%%2B%s)&limit=1",
			b.Config.SupabaseURL, cleanPhone, cleanPhone)
	}

	req, err := http.NewRequest("GET", queryURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create query for recipient %s: %v", clean, err)
	}
	req.Header.Set("apikey", b.Config.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+b.Config.SupabaseServiceRoleKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("recipient lookup HTTP error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("recipient lookup returned HTTP %d", resp.StatusCode)
	}

	var users []struct {
		CircleWalletAddress string `json:"circle_wallet_address"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&users); err == nil && len(users) > 0 && users[0].CircleWalletAddress != "" {
		return users[0].CircleWalletAddress, nil
	}

	return "", fmt.Errorf("no registered Rova wallet found for recipient '%s'", clean)
}

func (b *MeowBot) bindUserWithToken(phone, token string) (string, bool) {
	if b.Config.SupabaseURL == "" || b.Config.SupabaseServiceRoleKey == "" {
		return "", false
	}

	cleanToken := strings.TrimSpace(token)
	tokenHash := sha256.Sum256([]byte(cleanToken))
	queryURL := fmt.Sprintf("%s/rest/v1/whatsapp_link_tokens?select=email,expires_at&token_hash=eq.%s&limit=1",
		b.Config.SupabaseURL, hex.EncodeToString(tokenHash[:]))

	req, err := http.NewRequest("GET", queryURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("apikey", b.Config.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+b.Config.SupabaseServiceRoleKey)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode >= 400 {
		return "", false
	}
	defer resp.Body.Close()

	var records []struct {
		Email     string `json:"email"`
		ExpiresAt string `json:"expires_at"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil || len(records) == 0 {
		return "", false
	}

	record := records[0]
	if t, err := time.Parse(time.RFC3339, record.ExpiresAt); err == nil {
		if time.Now().After(t) {
			log.Printf("[MeowBot] WhatsApp link token expired at %v", t)
			return "", false
		}
	}

	cleanPhone := strings.TrimPrefix(phone, "+")
	formattedPhone := "+" + cleanPhone

	// Update Supabase users table for this user email (ONLY use whatsapp_number column)
	updateBody, _ := json.Marshal(map[string]string{
		"whatsapp_number": formattedPhone,
	})

	escapedEmail := url.QueryEscape(record.Email)
	updateURL := fmt.Sprintf("%s/rest/v1/users?email=eq.%s", b.Config.SupabaseURL, escapedEmail)
	patchReq, err := http.NewRequest("PATCH", updateURL, strings.NewReader(string(updateBody)))
	if err == nil {
		patchReq.Header.Set("apikey", b.Config.SupabaseServiceRoleKey)
		patchReq.Header.Set("Authorization", "Bearer "+b.Config.SupabaseServiceRoleKey)
		patchReq.Header.Set("Content-Type", "application/json")
		patchReq.Header.Set("Prefer", "return=minimal")
		if patchResp, err := client.Do(patchReq); err == nil {
			log.Printf("[MeowBot] Supabase PATCH response status: %d", patchResp.StatusCode)
			patchResp.Body.Close()
		}
	}

	deleteURL := fmt.Sprintf("%s/rest/v1/whatsapp_link_tokens?email=eq.%s", b.Config.SupabaseURL, escapedEmail)
	delReq, err := http.NewRequest("DELETE", deleteURL, nil)
	if err == nil {
		delReq.Header.Set("apikey", b.Config.SupabaseServiceRoleKey)
		delReq.Header.Set("Authorization", "Bearer "+b.Config.SupabaseServiceRoleKey)
		if delResp, err := client.Do(delReq); err == nil {
			delResp.Body.Close()
		}
	}

	log.Printf("[MeowBot] Bound phone %s to user %s", formattedPhone, record.Email)
	return record.Email, true
}

func (b *MeowBot) queuePendingAction(jid string, action pendingAction) (string, error) {
	if !b.executionAvailable() {
		return "", fmt.Errorf("WhatsApp execution is disabled")
	}

	randomBytes := make([]byte, 4)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}

	action.ConfirmationCode = strings.ToUpper(hex.EncodeToString(randomBytes))
	action.ExpiresAt = time.Now().Add(5 * time.Minute)
	b.pendingActionMu.Lock()
	b.pendingActions[jid] = action
	b.pendingActionMu.Unlock()
	return action.ConfirmationCode, nil
}

func (b *MeowBot) consumePendingAction(jid, code string) (pendingAction, bool) {
	b.pendingActionMu.Lock()
	defer b.pendingActionMu.Unlock()

	action, exists := b.pendingActions[jid]
	if !exists || time.Now().After(action.ExpiresAt) || !strings.EqualFold(action.ConfirmationCode, strings.TrimSpace(code)) {
		if exists && time.Now().After(action.ExpiresAt) {
			delete(b.pendingActions, jid)
		}
		return pendingAction{}, false
	}

	delete(b.pendingActions, jid)
	return action, true
}

func (b *MeowBot) executePendingAction(ctx context.Context, jid types.JID, action pendingAction) {
	if !b.executionAvailable() {
		b.replyText(jid, "⚠️ Fund-moving actions are currently disabled. No transaction was submitted.")
		return
	}

	var txHash string
	var err error

	switch action.Action {
	case "send":
		txHash, err = b.CircleClient.TransferUSDCFromWallet(ctx, action.WalletAddress, action.Recipient, action.Amount)
	case "swap":
		txHash, err = b.CircleClient.SwapStablecoinsWithWallet(ctx, action.WalletAddress, action.WalletAddress, action.BuyCurrency, action.Amount)
	case "bridge":
		txHash, err = b.CircleClient.BridgeCCTPWithWallet(ctx, action.WalletAddress, action.WalletAddress, action.DestinationChain, action.Amount)
	case "save":
		txHash, err = b.CircleClient.DepositSavingsVault(ctx, action.WalletAddress, action.WalletAddress, action.Amount)
	case "redeem":
		txHash, err = b.CircleClient.RedeemSavingsVault(ctx, action.WalletAddress, action.DepositID)
	default:
		b.replyText(jid, "❌ This confirmation does not describe a supported action.")
		return
	}

	if err != nil {
		log.Printf("[MeowBot] Confirmed %s action failed: %v", action.Action, err)
		b.replyText(jid, "❌ The confirmed action could not be completed. No further action was submitted.")
		return
	}

	b.replyText(jid, fmt.Sprintf("✅ *Rova Action Complete*\n\n• *Action*: %s\n• *Tx Hash*: `%s`\n\nhttps://testnet.arcscan.app/tx/%s", strings.ToUpper(action.Action), txHash, txHash))
}

func (b *MeowBot) executionAvailable() bool {
	return b.Config != nil && b.Config.ExecutionEnabled && b.Config.WhatsAppExecutionEnabled
}

func isFundMovingAction(action string) bool {
	switch action {
	case "send", "swap", "bridge", "save", "savings", "withdraw", "redeem":
		return true
	default:
		return false
	}
}

func parseDepositID(text string) (int64, bool) {
	for _, field := range strings.Fields(text) {
		depositID, err := strconv.ParseInt(field, 10, 64)
		if err == nil && depositID > 0 {
			return depositID, true
		}
	}
	return 0, false
}

func (b *MeowBot) getUserRulesStatus(phone string) string {
	if b.Config.SupabaseURL == "" || b.Config.SupabaseServiceRoleKey == "" {
		return "🎯 *Rova Target Rate Watchers*\n\n_No active FX target orders right now._"
	}

	cleanPhone := strings.TrimPrefix(phone, "+")
	queryURL := fmt.Sprintf("%s/rest/v1/agent_rules?select=id,pair,trigger_type,trigger_value,amount,status&notify_phone=eq.%%2B%s",
		b.Config.SupabaseURL, cleanPhone)

	req, err := http.NewRequest("GET", queryURL, nil)
	if err != nil {
		return "🎯 *Rova Target Rate Watchers*\n\n_No active FX target orders right now._"
	}
	req.Header.Set("apikey", b.Config.SupabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+b.Config.SupabaseServiceRoleKey)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode >= 400 {
		return "🎯 *Rova Target Rate Watchers*\n\n_No active FX target orders right now._"
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
		sb.WriteString("🎯 *Your Active FX Rate Targets*\n\n")
		for _, r := range rules {
			sb.WriteString(fmt.Sprintf("• *%.2f USDC → %s* when rate hits *%s %.4f*\n", r.Amount, r.Pair, r.TriggerType, r.TriggerValue))
		}
		sb.WriteString("\n_Rova is watching exchange rates 24/7 to execute automatically when your target rate is reached!_")
		return sb.String()
	}

	return "🎯 *Rova Target Rate Watchers*\n\n_You don't have any FX rate targets active right now._\n_Set a target rate anytime by asking me (e.g. \"Swap 100 USDC to EURC when rate hits 0.95\")!_"
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

func formatDisplayPhone(phone string) string {
	clean := strings.TrimSpace(phone)
	clean = strings.TrimLeft(clean, "+")
	if clean == "" {
		return ""
	}
	return "+" + clean
}

func (b *MeowBot) processIncomingCommand(ctx context.Context, jid types.JID, phone, text string) {
	cleanText := strings.TrimSpace(text)
	textLower := strings.ToLower(cleanText)
	displayPhone := formatDisplayPhone(phone)

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
					"• *Account*: %s\n"+
					"• *Phone*: %s\n"+
					"• *Network*: Arc Testnet\n\n"+
					"You can now manage your wallet and execute transactions directly in this chat!\n\n"+
					"Type *\"balance\"*, *\"status\"*, or *\"send 10 USDC to 0x...\"* to start.",
				userEmail, displayPhone,
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
	if userRecord != nil && userRecord.WhatsAppNumber != "" {
		displayPhone = formatDisplayPhone(userRecord.WhatsAppNumber)
	}

	if !registered {
		reply := fmt.Sprintf(
			"👋 *Hello! Welcome to Rova Financial Agent.*\n\n"+
				"To execute payments, swaps, and automated rules directly in this chat, please sign up and link your WhatsApp line on our web portal:\n\n"+
				"👉 *%s*\n\n"+
				"_Once activated on the portal, your WhatsApp line will be linked instantly!_",
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
			"👋 *Hello! Welcome to Rova Agent!*\n\n" +
				"You can manage your funds and target rate orders directly in this chat:\n\n" +
				"• *\"send 50 USDC to 0x...\"*\n" +
				"• *\"swap 100 USDC to EURC\"*\n" +
				"• *\"bridge 200 USDC from Ethereum to Arc\"*\n" +
				"• *\"balance\"* — View account wallet & balances\n" +
				"• *\"status\"* — View active FX rate targets\n\n" +
				"_Powered by Rova AI Engine_"

		b.replyText(jid, reply)
		return
	}

	boundWallet := ""
	if userRecord != nil {
		boundWallet = userRecord.CircleWalletAddress
	}

	jidKey := jid.String()

	if strings.HasPrefix(textLower, "confirm") {
		confirmationFields := strings.Fields(cleanText)
		if len(confirmationFields) != 2 {
			b.replyText(jid, "⚠️ Send `CONFIRM <code>` exactly as shown in the action preview.")
			return
		}

		action, confirmed := b.consumePendingAction(jidKey, confirmationFields[1])
		if !confirmed {
			b.replyText(jid, "⚠️ That confirmation code is invalid or expired. Start the action again to receive a fresh preview.")
			return
		}

		b.executePendingAction(ctx, jid, action)
		return
	}

	var fastParsed *ai.ParsedIntent
	if textLower == "balance" || textLower == "bal" || textLower == "my balance" || textLower == "balances" {
		fastParsed = &ai.ParsedIntent{Action: "balance", Reasoning: "Direct balance query"}
	} else if textLower == "status" || textLower == "orders" || textLower == "targets" || textLower == "rules" || textLower == "watchers" {
		fastParsed = &ai.ParsedIntent{Action: "status", Reasoning: "Direct target status query"}
	} else if textLower == "withdraw" || textLower == "redeem" || strings.HasPrefix(textLower, "withdraw") || strings.HasPrefix(textLower, "redeem") {
		fastParsed = &ai.ParsedIntent{Action: "withdraw", Reasoning: "Direct vault withdraw query"}
	}

	var parsed *ai.ParsedIntent
	if fastParsed != nil {
		parsed = fastParsed
	} else {
		p, err := b.AIParser.ParseIntent(ctx, cleanText)
		if err != nil {
			log.Printf("[MeowBot] AI Parsing fallback error: %v", err)
		}
		parsed = p
	}

	if parsed == nil {
		parsed = &ai.ParsedIntent{
			Action:    "help",
			Reasoning: "Fallback intent parser",
		}
	}

	log.Printf("[MeowBot] AI Parsed Action: %s, Amount: %.2f, Recipient: %s, Reasoning: %s", parsed.Action, parsed.Amount, parsed.Recipient, parsed.Reasoning)

	if boundWallet == "" && userRecord != nil {
		boundWallet = userRecord.CircleWalletAddress
	}
	if isFundMovingAction(parsed.Action) && !b.executionAvailable() {
		b.replyText(jid, "⚠️ Fund-moving actions are currently disabled. Use the authenticated web portal for account information; no transaction can be submitted from WhatsApp.")
		return
	}

	switch parsed.Action {
	case "balance":
		if boundWallet == "" {
			b.replyText(jid, fmt.Sprintf("⚠️ No wallet linked to your account yet.\n\nPlease log in to *%s* to set up your wallet.", appURL))
			return
		}

		chainClient, err := chain.NewChainClient(b.Config)
		if err != nil {
			b.replyText(jid, "❌ Unable to fetch balances right now. Please try again shortly.")
			return
		}

		usdcBal, _ := chainClient.GetBalanceUSDCWithFailover(ctx, boundWallet)
		eurcBal, _ := chainClient.GetBalanceEURCWithFailover(ctx, boundWallet)

		reply := fmt.Sprintf(
			"💰 *Your Balances*\n\n"+
				"• *USDC*: %.2f\n"+
				"• *EURC*: %.2f\n\n"+
				"🌐 *Network*: Arc Testnet\n"+
				"🔗 *Explorer*: https://testnet.arcscan.app/address/%s",
			usdcBal, eurcBal, boundWallet,
		)
		b.replyText(jid, reply)

	case "status":
		rulesSummary := b.getUserRulesStatus(phone)
		b.replyText(jid, rulesSummary)

	case "send":
		if boundWallet == "" {
			b.replyText(jid, fmt.Sprintf("⚠️ *Wallet Not Bound*: Unable to send funds because no wallet is linked to your account. Please log in to connect your wallet."))
			return
		}

		targetRecipient := parsed.Recipient
		if targetRecipient == "" {
			b.replyText(jid, "❌ *Recipient Missing*: Please specify a destination wallet address, email, or phone number (e.g. *\"send 50 USDC to user@example.com\"*).")
			return
		}

		resolvedWallet, err := b.resolveRecipientAddress(targetRecipient)
		if err != nil {
			log.Printf("[MeowBot Error] Recipient lookup failed for %s: %v", targetRecipient, err)
			b.replyText(jid, "❌ *Recipient Not Found*: The specified recipient is not registered on Rova or the address is invalid. Please double check and try again.")
			return
		}

		sendAmount := parsed.Amount
		if sendAmount <= 0 || sendAmount > b.Config.MaxWhatsAppActionAmount {
			b.replyText(jid, fmt.Sprintf("⚠️ Specify an amount greater than 0 and no more than %.0f USDC.", b.Config.MaxWhatsAppActionAmount))
			return
		}

		confirmationCode, err := b.queuePendingAction(jidKey, pendingAction{
			Action:        "send",
			WalletAddress: boundWallet,
			Recipient:     resolvedWallet,
			Amount:        sendAmount,
		})
		if err != nil {
			log.Printf("[MeowBot] Failed to prepare transfer confirmation: %v", err)
			b.replyText(jid, "❌ Unable to prepare the transfer confirmation. Please try again.")
			return
		}

		b.replyText(jid, fmt.Sprintf("⚠️ *Transfer Confirmation Required*\n\n• *Amount*: %.2f USDC\n• *Recipient*: `%s`\n\nReply `CONFIRM %s` within 5 minutes to submit this transfer.", sendAmount, resolvedWallet, confirmationCode))

	case "swap":
		if boundWallet == "" {
			b.replyText(jid, "⚠️ *Wallet Not Bound*: Unable to swap because no wallet is linked to your account.")
			return
		}

		swapAmount := parsed.Amount
		if swapAmount <= 0 || swapAmount > b.Config.MaxWhatsAppActionAmount {
			b.replyText(jid, fmt.Sprintf("⚠️ Specify an amount greater than 0 and no more than %.0f USDC.", b.Config.MaxWhatsAppActionAmount))
			return
		}

		buyCurr := parsed.Currency
		if buyCurr == "" {
			buyCurr = "EURC"
		}
		buyCurr = strings.ToUpper(buyCurr)
		if buyCurr != "USDC" && buyCurr != "EURC" {
			b.replyText(jid, "⚠️ Only USDC and EURC swaps are supported.")
			return
		}

		confirmationCode, err := b.queuePendingAction(jidKey, pendingAction{
			Action:        "swap",
			WalletAddress: boundWallet,
			BuyCurrency:   buyCurr,
			Amount:        swapAmount,
		})
		if err != nil {
			log.Printf("[MeowBot] Failed to prepare swap confirmation: %v", err)
			b.replyText(jid, "❌ Unable to prepare the swap confirmation. Please try again.")
			return
		}

		b.replyText(jid, fmt.Sprintf("⚠️ *Swap Confirmation Required*\n\n• *Sell*: %.2f USDC\n• *Buy*: %s\n\nReply `CONFIRM %s` within 5 minutes to submit this swap.", swapAmount, buyCurr, confirmationCode))

	case "bridge":
		if boundWallet == "" {
			b.replyText(jid, "⚠️ *Wallet Not Bound*: Unable to bridge because no wallet is linked to your account.")
			return
		}

		bridgeAmount := parsed.Amount
		if bridgeAmount <= 0 || bridgeAmount > b.Config.MaxWhatsAppActionAmount {
			b.replyText(jid, fmt.Sprintf("⚠️ Specify an amount greater than 0 and no more than %.0f USDC.", b.Config.MaxWhatsAppActionAmount))
			return
		}

		destinationChain := parsed.SourceChain
		if destinationChain == "" {
			destinationChain = "Ethereum"
		}
		destinationChain = strings.ToLower(destinationChain)
		if destinationChain != "ethereum" && destinationChain != "base" {
			b.replyText(jid, "⚠️ Only Ethereum Sepolia and Base Sepolia bridge routes are supported.")
			return
		}

		confirmationCode, err := b.queuePendingAction(jidKey, pendingAction{
			Action:        "bridge",
			WalletAddress: boundWallet,
			DestinationChain: destinationChain,
			Amount:        bridgeAmount,
		})
		if err != nil {
			log.Printf("[MeowBot] Failed to prepare bridge confirmation: %v", err)
			b.replyText(jid, "❌ Unable to prepare the bridge confirmation. Please try again.")
			return
		}

		b.replyText(jid, fmt.Sprintf("⚠️ *Bridge Confirmation Required*\n\n• *Amount*: %.2f USDC\n• *Route*: Arc → %s\n\nReply `CONFIRM %s` within 5 minutes to submit this bridge.", bridgeAmount, destinationChain, confirmationCode))

	case "save", "savings":
		if boundWallet == "" {
			b.replyText(jid, "⚠️ *Wallet Not Bound*: Unable to manage savings because no wallet is linked to your account.")
			return
		}

		// 1. Check if user requested a standing percentage intent (e.g. "always save 10% of every deposit")
		isStanding := strings.Contains(textLower, "always") || strings.Contains(textLower, "%") ||
			strings.Contains(textLower, "every") || strings.Contains(textLower, "whenever") || strings.Contains(textLower, "standing")

		if isStanding {
			b.replyText(jid, "⚠️ *Automatic savings cannot be armed through WhatsApp.* Create it in the authenticated web portal, where it is subject to scheduler policy limits and audit controls.")
			return
		}

		// 2. Otherwise execute immediate one-off savings deposit
		saveAmount := parsed.Amount
		if saveAmount <= 0 || saveAmount > b.Config.MaxWhatsAppActionAmount {
			b.replyText(jid, fmt.Sprintf("⚠️ Specify an amount greater than 0 and no more than %.0f USDC.", b.Config.MaxWhatsAppActionAmount))
			return
		}

		confirmationCode, err := b.queuePendingAction(jidKey, pendingAction{
			Action:        "save",
			WalletAddress: boundWallet,
			Amount:        saveAmount,
		})
		if err != nil {
			log.Printf("[MeowBot] Failed to prepare savings confirmation: %v", err)
			b.replyText(jid, "❌ Unable to prepare the savings confirmation. Please try again.")
			return
		}

		b.replyText(jid, fmt.Sprintf("⚠️ *Savings Deposit Confirmation Required*\n\n• *Amount*: %.2f USDC\n• *Timelock*: 30 days\n\nReply `CONFIRM %s` within 5 minutes to submit this vault deposit.", saveAmount, confirmationCode))

	case "withdraw", "redeem":
		depositID, found := parseDepositID(cleanText)
		if !found {
			b.replyText(jid, "⚠️ Specify the exact deposit ID to redeem, for example: `redeem 3`.")
			return
		}

		confirmationCode, err := b.queuePendingAction(jidKey, pendingAction{
			Action:        "redeem",
			WalletAddress: boundWallet,
			DepositID:     depositID,
		})
		if err != nil {
			log.Printf("[MeowBot] Failed to prepare redemption confirmation: %v", err)
			b.replyText(jid, "❌ Unable to prepare the redemption confirmation. Please try again.")
			return
		}

		b.replyText(jid, fmt.Sprintf("⚠️ *Savings Redemption Confirmation Required*\n\n• *Deposit ID*: #%d\n• *Destination*: your managed wallet\n\nReply `CONFIRM %s` within 5 minutes to request redemption. The contract will reject still-locked deposits.", depositID, confirmationCode))

	default:
		reply :=
			"🤖 *Rova Assistant*\n\n" +
				"How can I help you today? You can ask me to:\n" +
				"• *\"send 50 USDC to 0x...\"*\n" +
				"• *\"swap 100 USDC to EURC\"*\n" +
				"• *\"bridge 200 USDC from Ethereum\"*\n" +
				"• *\"save 25 USDC\"*\n" +
				"• *\"balance\"* or *\"status\"*"

		b.replyText(jid, reply)
	}
}

func (b *MeowBot) SendMessage(toPhone, text string) error {
	return b.SendMessageToPhone(toPhone, text)
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

func (b *MeowBot) SendExecutionReport(phone string, opts agent.NotificationOpts) error {
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
	if err := cfg.ValidateDatabaseAccess(); err != nil {
		return err
	}

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

	chainClient, err := chain.NewChainClient(cfg)
	if err != nil {
		log.Printf("[MeowBot] Warning: Failed to init ChainClient for Watcher: %v", err)
	}

	store := agent.NewSupabaseStore(cfg.SupabaseURL, cfg.SupabaseServiceRoleKey)
	shopper := nanopay.NewShopper()
	interval := time.Duration(cfg.BalancePollInterval) * time.Second
	if interval <= 0 {
		interval = 180 * time.Second
	}

	if chainClient != nil {
		watcher := agent.NewWatcherEngine(store, chainClient, shopper, bot, interval)
		watcher.StartWatcher(ctx)
	}

	log.Println("[MeowBot] Rova WhatsApp Agent Service & 24/7 Supabase Watcher Engine is RUNNING.")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("[MeowBot] Shutting down WhatsApp service gracefully...")
	bot.Stop()
	return nil
}
