package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"rova-agent-go/pkg/ai"
	"rova-agent-go/pkg/circle"
	"rova-agent-go/pkg/config"
	"rova-agent-go/pkg/nanopay"
)

func main() {
	envFiles := []string{".env.local", ".env", "../.env.local", "../.env", "../../rova/.env.local"}
	for _, file := range envFiles {
		_ = godotenv.Overload(file)
	}

	promptFlag := flag.String("prompt", "send 50 USDC to 0x71C7656EC7ab88b098defB751B7401B5f6d8976F", "Natural language intent prompt to test")
	flag.Parse()

	cfg := config.LoadConfig()
	prompt := strings.TrimSpace(*promptFlag)

	fmt.Println("==================================================================")
	fmt.Println("             ROVA AGENT GO — PURE AI AGENT TESTER                 ")
	fmt.Println("==================================================================")
	fmt.Printf("📥 Input Prompt:        \"%s\"\n", prompt)
	modelDisp := cfg.AIModel
	if modelDisp == "" {
		modelDisp = "Default per provider"
	}
	fmt.Printf("⚙️ Allow Regex Fallback: %t (config.yaml)\n", cfg.AllowRegexFallback)
	fmt.Printf("⚙️ AI Provider:         %s (config.yaml)\n", cfg.AIProvider)
	fmt.Printf("⚙️ AI Model Configured: %s (config.yaml)\n\n", modelDisp)

	// 1. Query AI Agent with config setting
	fmt.Println("🤖 1. Querying Pure AI Agent (Gemini / Claude / AgentRouter)...")
	parser := ai.NewAIParserWithConfig(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	parsed, err := parser.ParseIntentWithFallback(ctx, prompt, cfg.AllowRegexFallback)
	if err != nil {
		fmt.Printf("❌ STRICT AI AGENT ERROR: %v\n", err)
	} else if parsed != nil {
		fmt.Printf("   ✅ Action:      %s\n", parsed.Action)
		fmt.Printf("   ✅ Amount:      %.2f %s\n", parsed.Amount, parsed.Currency)
		fmt.Printf("   ✅ Recipient:   %s\n", parsed.Recipient)
		fmt.Printf("   ✅ Reasoning:   %s\n", parsed.Reasoning)
	}
	fmt.Println()

	// 2. Test Goroutine Nanopayments Shopper
	fmt.Println("🏷️ 2. Testing Goroutine x402 Nanopayments Rate Shopper...")
	shopper := nanopay.NewShopper()
	shopResult := shopper.ShopRates("USDC/EURC")
	fmt.Printf("   ✅ Providers Checked: %d\n", shopResult.ProvidersChecked)
	fmt.Printf("   ✅ Best Provider:     %s (Rate: %.4f)\n", shopResult.BestProvider, shopResult.BestRate)
	fmt.Println()

	// 3. Test Circle Wallet Credentials
	fmt.Println("💳 3. Testing Circle Wallet API Integration...")
	circleClient := circle.NewCircleClient(cfg)
	if cfg.CircleAPIKey != "" {
		fmt.Println("   ✅ CIRCLE_API_KEY detected.")
	} else {
		fmt.Println("   ⚠️ CIRCLE_API_KEY missing (Running in Mock Execution Mode).")
	}

	if parsed != nil && (parsed.Action == "send" || parsed.Action == "swap") {
		fmt.Printf("   🚀 Simulating Agent Execution of %.2f USDC...\n", parsed.Amount)
		txHash, err := circleClient.TransferUSDC(ctx, parsed.Recipient, parsed.Amount)
		if err != nil {
			fmt.Printf("   ⚠️ Transfer Simulation Error: %v\n", err)
		} else {
			fmt.Printf("   ✅ Simulated Tx Hash: %s\n", txHash)
			fmt.Printf("   🔗 ArcScan URL:       https://testnet.arcscan.app/tx/%s\n", txHash)
		}
	}

	fmt.Println()
	fmt.Println("==================================================================")
	fmt.Println("                STRICT AGENT TEST COMPLETE                        ")
	fmt.Println("==================================================================")
}
