package main

import (
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"

	"rova-agent-go/pkg/config"
	"rova-agent-go/pkg/whatsapp"
)

func main() {
	fmt.Println("==================================================================")
	fmt.Println("             ROVA AGENT GO — STANDALONE WHATSMEOW BOT             ")
	fmt.Println("==================================================================")

	if err := godotenv.Load(".env.local"); err != nil {
		godotenv.Load(".env")
	}

	cfg := config.LoadConfig()
	log.Printf("[Main] Config loaded. CircleWalletID=%s", cfg.CircleWalletID)

	if err := whatsapp.RunMeowBotService(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error running WhatsApp bot service: %v\n", err)
		os.Exit(1)
	}
}
