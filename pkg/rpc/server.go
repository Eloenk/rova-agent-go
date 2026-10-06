package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"rova-agent-go/pkg/agent"
	"rova-agent-go/pkg/chain"
	"rova-agent-go/pkg/config"
)

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      interface{}     `json:"id"`
}

type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
	ID      interface{} `json:"id"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type RPCServer struct {
	Config      *config.Config
	Store       *agent.Store
	ChainClient *chain.ChainClient
}

func NewRPCServer(cfg *config.Config, store *agent.Store, chainClient *chain.ChainClient) *RPCServer {
	return &RPCServer{
		Config:      cfg,
		Store:       store,
		ChainClient: chainClient,
	}
}

func (s *RPCServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != "POST" {
		sendError(w, nil, -32600, "Invalid Request: RPC requires HTTP POST")
		return
	}

	var req JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendError(w, nil, -32700, "Parse error: Invalid JSON")
		return
	}

	if req.JSONRPC != "2.0" {
		sendError(w, req.ID, -32600, "Invalid Request: jsonrpc version must be 2.0")
		return
	}

	result, err := s.handleMethod(r.Context(), req.Method, req.Params)
	if err != nil {
		sendError(w, req.ID, -32603, err.Error())
		return
	}

	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  result,
		ID:      req.ID,
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *RPCServer) handleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {

	case "rova_getHealth":
		return map[string]interface{}{
			"status":        "healthy",
			"engine":        "rova-agent-go",
			"executionMode": s.Config.ExecutionMode,
			"arcChain":      s.Config.ChainID,
			"wallet":        s.ChainClient.Address.Hex(),
			"activeRules":   len(s.Store.ListActiveRules()),
		}, nil

	case "rova_listRules":
		return s.Store.ListActiveRules(), nil

	case "rova_listExecutions":
		return s.Store.ListExecutions(), nil

	default:
		return nil, fmt.Errorf("method not found: %s", method)
	}
}

func sendError(w http.ResponseWriter, id interface{}, code int, message string) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		Error: &RPCError{
			Code:    code,
			Message: message,
		},
		ID: id,
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}
