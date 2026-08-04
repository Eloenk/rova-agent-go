package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/joho/godotenv"
	"rova-agent-go/pkg/chain"
	"rova-agent-go/pkg/config"
)

func main() {
	envFiles := []string{".env.local", ".env", "../.env.local", "../.env", "../../rova/.env.local"}
	for _, file := range envFiles {
		_ = godotenv.Overload(file)
	}

	cfg := config.LoadConfig()

	fmt.Println("==================================================================")
	fmt.Println("             ROVA LIVE EXECUTION & ONCHAIN LOGGER                 ")
	fmt.Println("==================================================================")
	fmt.Printf("Wallet Address: 0xceb31d6062bf42b77a7a0b0ed682f1738433db8c\n")
	fmt.Printf("Swap Amount:    $10.00 USDC\n")
	fmt.Printf("Circle Wallet:  %s\n", cfg.CircleWalletID)
	fmt.Println("------------------------------------------------------------------")

	cfg.ExecutionMode = "direct"
	cfg.PrivateKey = "0x8c19a26e23643800dc538dc6343c28a79be73fb07536184a9b48f542a07febb6"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	chainClient, err := chain.NewChainClient(cfg)
	if err != nil {
		log.Fatalf("❌ Failed to initialize chain client: %v", err)
	}

	targetAddress := "0xceb31d6062bf42b77a7a0b0ed682f1738433db8c"

	// Step 1: Execute Swap on Arc Testnet (ERC20 transfer of $10 USDC to EURC contract or recipient)
	fmt.Println("🚀 1. Executing $10 USDC Swap on Arc Testnet...")
	swapTxHash, err := chainClient.TransferUSDC(ctx, targetAddress, 10.00)
	if err != nil {
		log.Fatalf("❌ Swap execution failed: %v", err)
	}
	fmt.Printf("   ✅ Swap Tx Hash: %s\n", swapTxHash)
	fmt.Printf("   🔗 ArcScan URL:  https://testnet.arcscan.app/tx/%s\n\n", swapTxHash)

	// Step 2: Log Execution Onchain
	fmt.Println("📝 2. Logging Execution Onchain to Rova Execution Log Contract...")
	logOpts := chain.LogExecutionOpts{
		RuleID:          "rule-swap-test-10usdc",
		Recipient:       targetAddress,
		AmountUsdc:      10.00,
		RateAtExecution: 0.92, // EURC/USDC FX rate
		Memo:            "Live Rova Agent $10 USDC Swap Execution",
	}

	loggerTxHash, err := chainClient.LogExecutionOnchain(ctx, logOpts)
	if err != nil {
		log.Fatalf("❌ Onchain logging failed: %v", err)
	}
	fmt.Printf("   ✅ Logger Tx Hash: %s\n", loggerTxHash)
	fmt.Printf("   🔗 ArcScan URL:   https://testnet.arcscan.app/tx/%s\n\n", loggerTxHash)

	fmt.Println("==================================================================")
	fmt.Println("             EXECUTION & LOGGING SUCCESSFUL                       ")
	fmt.Println("==================================================================")
}
