package main

import (
	"fmt"
	"log"
	"os"

	"rova-agent-go/pkg/config"
	"rova-agent-go/pkg/whatsapp"
)

func main() {
	fmt.Println("==================================================================")
	fmt.Println("             ROVA AGENT GO — STANDALONE WHATSMEOW BOT             ")
	fmt.Println("==================================================================")

	cfg := config.LoadConfig()
	log.Printf("[Main] Configuration loaded")

	if err := whatsapp.RunMeowBotService(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error running WhatsApp bot service: %v\n", err)
		os.Exit(1)
	}
}
