package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"rova-agent-go/pkg/ai"
	"rova-agent-go/pkg/config"
)

func loadEnvFile(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			val = strings.Trim(val, "\r\n\"'")
			val = strings.TrimSpace(val)
			if os.Getenv(key) == "" {
				os.Setenv(key, val)
			}
		}
	}
}

func main() {
	fmt.Println("===================================================")
	fmt.Println("    Rova Go Backend — NVIDIA NIM API Test Suite    ")
	fmt.Println("===================================================")

	// Try loading env variables
	loadEnvFile(".env")
	loadEnvFile(".env.local")
	loadEnvFile("../rova/.env.local")
	loadEnvFile("../rova/.env")

	nvKey := strings.Trim(strings.TrimSpace(os.Getenv("NVIDIA_API_KEY")), "\r\n\"'")
	os.Setenv("NVIDIA_API_KEY", nvKey)
	if nvKey == "" {
		log.Fatalf("❌ ERROR: NVIDIA_API_KEY environment variable is not set!")
	}

	fmt.Printf("✔ Loaded NVIDIA_API_KEY (Length: %d, Prefix: %s...)\n", len(nvKey), nvKey[:10])

	cfg := config.LoadConfig()
	cfg.AIProvider = "auto"
	parser := ai.NewAIParserWithConfig(cfg)
	testPrompts := []string{
		"Send 15.5 USDC to 0x33c50a793fd2fa02ed0b54196ab4f1faf7bad046",
		"Swap 100 USDC to EURC on Arc Testnet",
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
