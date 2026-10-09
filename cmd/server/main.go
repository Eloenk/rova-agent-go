package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"

	"syscall"
	"time"

	"rova-agent-go/pkg/agent"
	"rova-agent-go/pkg/chain"
	"rova-agent-go/pkg/config"
	"rova-agent-go/pkg/nanopay"
	"rova-agent-go/pkg/rpc"
	"rova-agent-go/pkg/security"
	"rova-agent-go/pkg/whatsapp"
)

func main() {
	cfg := config.LoadConfig()
	if err := cfg.ValidateEngineServer(); err != nil {
		log.Fatalf("Engine configuration rejected: %v", err)
	}

	chainClient, err := chain.NewChainClient(cfg)
	if err != nil {
		log.Fatalf("Fatal error initializing ethclient: %v", err)
	}

	store := agent.NewSupabaseStore(cfg.SupabaseURL, cfg.SupabaseServiceRoleKey)
	shopper := nanopay.NewShopper()
	notifier := whatsapp.NewNotifier(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := agent.NewWatcherEngine(store, chainClient, shopper, notifier, 5*time.Second)
	watcher.StartWatcher(ctx)

	rpcServer := rpc.NewRPCServer(cfg, store, chainClient)
	mux := http.NewServeMux()
	mux.Handle("/rpc", security.RequireBearer(cfg.EngineAPIToken, rpcServer))

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "healthy",
			"engine": "rova-agent-go",
			"arcChain": cfg.ChainID,
		})
	})

	rulesHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(store.ListActiveRules())
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	mux.Handle("/api/agent/rules", security.RequireBearer(cfg.EngineAPIToken, rulesHandler))

	executionsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(store.ListExecutions())
	})
	mux.Handle("/api/agent/executions", security.RequireBearer(cfg.EngineAPIToken, executionsHandler))

	serverAddr := cfg.BindAddress + ":" + cfg.Port
	server := &http.Server{
		Addr:              serverAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

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
