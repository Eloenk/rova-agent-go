package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"rova-agent-go/pkg/agent"
	"rova-agent-go/pkg/config"
)

func main() {
	fmt.Println("===================================================")
	fmt.Println("   Rova Go — Supabase Realtime WSS Test Suite     ")
	fmt.Println("===================================================")

	cfg := config.LoadConfig()
	if cfg.SupabaseURL == "" || cfg.SupabaseSecretKey == "" {
		log.Fatalf("❌ ERROR: SUPABASE_URL or SUPABASE_SECRET_KEY is missing!")
	}

	fmt.Println("✔ Supabase configuration is present")

	store := agent.NewSupabaseStore(cfg.SupabaseURL, cfg.SupabaseSecretKey, cfg.SupabasePublishableKey)
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
