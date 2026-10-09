package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"rova-agent-go/pkg/agent"
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
	fmt.Println("   Rova Go — Supabase Realtime WSS Test Suite     ")
	fmt.Println("===================================================")

	loadEnvFile(".env.local")
	loadEnvFile(".env")
	loadEnvFile("../rova/.env.local")
	loadEnvFile("../rova/.env")

	cfg := config.LoadConfig()
	if cfg.SupabaseURL == "" || cfg.SupabaseServiceRoleKey == "" {
		log.Fatalf("❌ ERROR: SUPABASE_URL or SUPABASE_SERVICE_ROLE_KEY is missing!")
	}

	fmt.Printf("✔ Loaded Supabase URL: %s\n", cfg.SupabaseURL)
	fmt.Println("✔ Loaded Supabase service credentials\n")

	store := agent.NewSupabaseStore(cfg.SupabaseURL, cfg.SupabaseServiceRoleKey)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	fmt.Println("⏳ Starting Realtime WebSocket Phoenix channel subscription...")
	store.StartRealtimeSubscription(ctx)

	fmt.Println("\n📡 Listening for real-time Postgres change events (agent_rules / standing_intents)...")
	fmt.Println("   (Try adding or updating a rule on Supabase / Rova Web Portal during this 15-second window!)")

	<-time.After(15 * time.Second)

	activeRules := store.ListActiveRules()
	activeIntents := store.ListActiveStandingIntents()

	fmt.Printf("\n✅ Test Complete! Memory Cache State:\n")
	fmt.Printf("  • Active Agent Rules:            %d\n", len(activeRules))
	fmt.Printf("  • Active Standing Intents:     %d\n", len(activeIntents))
	fmt.Println("===================================================")
}
