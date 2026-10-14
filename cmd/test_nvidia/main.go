package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"rova-agent-go/pkg/ai"
	"rova-agent-go/pkg/config"
)

func main() {
	fmt.Println("===================================================")
	fmt.Println("    Rova Go Backend — NVIDIA NIM API Test Suite    ")
	fmt.Println("===================================================")

	cfg := config.LoadConfig()
	if cfg.NvidiaAPIKey == "" {
		log.Fatalf("❌ ERROR: NVIDIA_API_KEY environment variable is not set!")
	}

	fmt.Println("✔ NVIDIA_API_KEY is configured")

	cfg.AIProvider = "nvidia"
	parser := ai.NewAIParserWithConfig(cfg)
	testPrompts := []string{
		"Send 15.5 USDC to 0x33c50a793fd2fa02ed0b54196ab4f1faf7bad046",
		"Plan a 100 USDC to EURC swap on Arc Mainnet",
		"Bridge 50 USDC from Ethereum to Arc",
	}

	for _, prompt := range testPrompts {
		fmt.Printf("\n🧪 Testing Intent: \"%s\"\n", prompt)
		start := time.Now()

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		intent, err := parser.ParseIntentStrict(ctx, prompt)
		cancel()
		duration := time.Since(start)

		if err != nil {
			fmt.Printf("✖ Failed (%v): %v\n", duration, err)
		} else {
			fmt.Printf("✔ Success (%v)!\n", duration)
			fmt.Printf("  • Action:    %s\n", intent.Action)
			fmt.Printf("  • Amount:    %.2f %s\n", intent.Amount, intent.Currency)
			if intent.Recipient != "" {
				fmt.Printf("  • Recipient: %s\n", intent.Recipient)
			}
			if intent.SourceChain != "" {
				fmt.Printf("  • Route:     %s -> %s\n", intent.SourceChain, intent.TargetChain)
			}
			fmt.Printf("  • Reasoning: %s\n", intent.Reasoning)
		}
		fmt.Println("---------------------------------------------------")
	}
}
