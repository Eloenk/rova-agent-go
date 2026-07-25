package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"rova-agent-go/pkg/agent"
	"rova-agent-go/pkg/chain"
	"rova-agent-go/pkg/config"
	"rova-agent-go/pkg/nanopay"
	"rova-agent-go/pkg/rpc"
	"rova-agent-go/pkg/whatsapp"
)

func main() {
	cfg := config.LoadConfig()

	chainClient, err := chain.NewChainClient(cfg)
	if err != nil {
		log.Fatalf("Fatal error initializing ethclient: %v", err)
	}

	store := agent.NewStore()
	shopper := nanopay.NewShopper()
	notifier := whatsapp.NewNotifier(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := agent.NewWatcherEngine(store, chainClient, shopper, notifier, 5*time.Second)
	watcher.StartWatcher(ctx)

	demoRule := &agent.AgentRule{
		ID:                  "wa-rule-demo-1",
		CreatedAt:           time.Now(),
		Status:              agent.StatusActive,
		RecipientLabel:      "Sister (Remittance)",
		RecipientIdentifier: "0xfe4f5d1ceeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		Amount:              100.0,
		Pair:                "USDC/EURC",
		TriggerType:         agent.TriggerRateGTE,
		TriggerValue:        0.940,
		CustodyMode:         agent.CustodyManaged,
		SourceWallet:        chainClient.Address.Hex(),
		NotifyPhone:         "+254712345678",
		SourceChannel:       "whatsapp",
	}
	store.AddRule(demoRule)

	rpcServer := rpc.NewRPCServer(cfg, store, chainClient)
	http.Handle("/rpc", rpcServer)

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":        "healthy",
			"engine":        "rova-agent-go",
			"executionMode": cfg.ExecutionMode,
			"arcChain":      cfg.ChainID,
			"wallet":        chainClient.Address.Hex(),
			"mockMode":      cfg.MockMode,
			"activeRules":   len(store.ListActiveRules()),
		})
	})

	http.HandleFunc("/api/agent/rules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(store.ListActiveRules())
			return
		}
		if r.Method == "POST" {
			var newRule agent.AgentRule
			if err := json.NewDecoder(r.Body).Decode(&newRule); err != nil {
				http.Error(w, "Invalid JSON", http.StatusBadRequest)
				return
			}
			newRule.ID = fmt.Sprintf("rule-%d", time.Now().UnixNano())
			newRule.CreatedAt = time.Now()
			newRule.Status = agent.StatusActive
			store.AddRule(&newRule)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(newRule)
			return
		}
	})

	http.HandleFunc("/api/agent/executions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(store.ListExecutions())
	})

	http.HandleFunc("/api/whatsapp/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			mode := r.URL.Query().Get("hub.mode")
			token := r.URL.Query().Get("hub.verify_token")
			challenge := r.URL.Query().Get("hub.challenge")

			if mode == "subscribe" && token == cfg.WhatsAppVerifyToken {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(challenge))
				return
			}
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		if r.Method == "POST" {
			var body struct {
				From string `json:"from"`
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.From != "" {
				if strings.ToLower(body.Text) == "status" {
					activeCount := len(store.ListActiveRules())
					reply := fmt.Sprintf("📊 *Rova Go Agent Status*\n\nActive Rate Watchers: %d\nWallet: `%s`", activeCount, chainClient.Address.Hex())
					notifier.SendMessage(body.From, reply)
				}
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "received"})
			return
		}
	})

	serverAddr := ":" + cfg.Port
	server := &http.Server{Addr: serverAddr}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	cancel()

	ctxShutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()

	if err := server.Shutdown(ctxShutdown); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}
}
