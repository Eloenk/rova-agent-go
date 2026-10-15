package main

import (
	"context"
	"flag"
	"fmt"
	"rova-agent-go/pkg/chain"
	"rova-agent-go/pkg/config"
)

func main() {
	walletFlag := flag.String("wallet", "", "Arc wallet address to query")
	flag.Parse()
	if *walletFlag == "" {
		fmt.Println("Usage: go run ./cmd/test-eurc -wallet 0x...")
		return
	}

	cfg := config.LoadConfig()
	fmt.Println("=== Rova Balance Check Test ===")
	fmt.Printf("Loaded from config.yaml:\n")
	fmt.Printf("  USDC Contract: %s\n", cfg.USDCContractAddress)
	fmt.Printf("  EURC Contract: %s\n", cfg.EURCContractAddress)
	fmt.Printf("  RPC URLs:      %v\n\n", cfg.ArcRPCURLs)

	client, err := chain.NewChainClient(cfg)
	if err != nil {
		fmt.Printf("Error initializing ChainClient: %v\n", err)
		return
	}

	wallet := *walletFlag
	balUsdc, errUsdc := client.GetBalanceUSDCWithFailover(context.Background(), wallet)
	balEurc, errEurc := client.GetBalanceEURCWithFailover(context.Background(), wallet)

	fmt.Printf("Wallet: %s\n", wallet)
	fmt.Printf("  USDC Balance: %.2f (err: %v)\n", balUsdc, errUsdc)
	fmt.Printf("  EURC Balance: %.2f (err: %v)\n", balEurc, errEurc)
}
