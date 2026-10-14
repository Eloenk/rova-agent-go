package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rova-agent-go/pkg/config"
)

type UserRecord struct {
	CircleWalletAddress string `json:"circle_wallet_address"`
}

type RPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type RPCResponse struct {
	Result string `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func fetchHTTP(urlStr string, headers map[string]string, postBody []byte) ([]byte, int, error) {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: false},
		TLSHandshakeTimeout: 10 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	}

	method := "GET"
	var reqBody io.Reader
	if len(postBody) > 0 {
		method = "POST"
		reqBody = bytes.NewBuffer(postBody)
	}

	req, err := http.NewRequest(method, urlStr, reqBody)
	if err != nil {
		return nil, 0, err
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return body, resp.StatusCode, err
	}

	return nil, 0, err
}

func getOnchainBalance(rpcURL, tokenAddress, walletAddress string) (float64, error) {
	cleanWallet := strings.TrimPrefix(walletAddress, "0x")
	if len(cleanWallet) != 40 {
		return 0.0, fmt.Errorf("invalid wallet address length")
	}

	// 0x70a08231 is selector for balanceOf(address)
	callData := "0x70a08231000000000000000000000000" + strings.ToLower(cleanWallet)

	payload := RPCRequest{
		JSONRPC: "2.0",
		Method:  "eth_call",
		Params: []interface{}{
			map[string]string{
				"to":   tokenAddress,
				"data": callData,
			},
			"latest",
		},
		ID: 1,
	}

	jsonBytes, _ := json.Marshal(payload)
	headers := map[string]string{"Content-Type": "application/json"}

	body, statusCode, err := fetchHTTP(rpcURL, headers, jsonBytes)
	if err != nil || statusCode != 200 {
		return 0.0, fmt.Errorf("RPC request error: %v", err)
	}

	var rpcRes RPCResponse
	if err := json.Unmarshal(body, &rpcRes); err != nil || rpcRes.Result == "" || rpcRes.Result == "0x" {
		return 0.0, nil
	}

	hexStr := strings.TrimPrefix(rpcRes.Result, "0x")
	rawInt := new(big.Int)
	rawInt.SetString(hexStr, 16)

	// USDC/EURC have 6 decimals on Arc Testnet
	balFloat := new(big.Float).SetInt(rawInt)
	decimals := new(big.Float).SetFloat64(1000000.0)
	result := new(big.Float).Quo(balFloat, decimals)

	val, _ := result.Float64()
	return val, nil
}

func main() {
	emailFlag := flag.String("email", "", "User email address to fetch Circle wallet for")
	flag.Parse()

	cfg := config.LoadConfig()

	fmt.Println("==================================================================")
	fmt.Println("         ROVA AGENT GO — CIRCLE WALLET FETCH TEST                 ")
	fmt.Println("==================================================================")

	targetEmail := strings.TrimSpace(strings.ToLower(*emailFlag))
	if targetEmail == "" {
		fmt.Println("An explicit -email value is required; refusing to enumerate user records.")
		return
	}

	// 1. Query Supabase for User's Circle Wallet Address
	var circleWallet string

	if cfg.SupabaseURL != "" && cfg.SupabaseSecretKey != "" {
		fmt.Printf("🔍 1. Querying the configured Supabase project...\n")

		parsedURL, err := url.Parse(cfg.SupabaseURL)
		if err != nil {
			fmt.Println("   ⚠️ Supabase URL is invalid.")
			return
		}
		parsedURL.Path = strings.TrimRight(parsedURL.Path, "/") + "/rest/v1/users"
		query := parsedURL.Query()
		query.Set("select", "circle_wallet_address")
		query.Set("email", "eq."+targetEmail)
		query.Set("limit", "1")
		parsedURL.RawQuery = query.Encode()
		queryURL := parsedURL.String()

		headers := map[string]string{
			"apikey":        cfg.SupabaseSecretKey,
			"Authorization": "Bearer " + cfg.SupabaseSecretKey,
		}

		bodyBytes, statusCode, err := fetchHTTP(queryURL, headers, nil)
		if err == nil && statusCode == 200 {
			var users []UserRecord
			if err := json.Unmarshal(bodyBytes, &users); err == nil && len(users) > 0 {
				circleWallet = users[0].CircleWalletAddress
				fmt.Printf("   ✅ Matching user wallet found.\n")
			} else {
				fmt.Printf("   ℹ️ No matching user found in database.\n")
			}
		} else {
			fmt.Printf("   ⚠️ Supabase HTTP query error: %v\n", err)
		}
	} else {
		fmt.Println("   ⚠️ Supabase environment variables not configured.")
	}

	// 2. Query Onchain Balances directly via Arc Testnet RPC
	if circleWallet != "" {
		fmt.Println("\n💰 2. Querying Arc Testnet RPC Onchain Balances...")

		usdcBal, errUsdc := getOnchainBalance(cfg.ArcRPCURL, cfg.USDCContractAddress, circleWallet)
		if errUsdc == nil {
			fmt.Printf("   ✅ USDC Balance: %.2f USDC\n", usdcBal)
		} else {
			fmt.Printf("   ⚠️ USDC Balance Error: %v\n", errUsdc)
		}

		eurcBal, errEurc := getOnchainBalance(cfg.ArcRPCURL, cfg.EURCContractAddress, circleWallet)
		if errEurc == nil {
			fmt.Printf("   ✅ EURC Balance: %.2f EURC\n", eurcBal)
		} else {
			fmt.Printf("   ⚠️ EURC Balance Error: %v\n", errEurc)
		}
	}

	// 3. Query Circle Developer API directly (if CIRCLE_API_KEY is configured)
	if cfg.CircleAPIKey != "" {
		fmt.Println("\n⭕ 3. Checking Circle Developer Controlled Wallets API...")
		circleURL := "https://api.circle.com/v1/w3s/developer/wallets"
		headers := map[string]string{
			"Authorization": "Bearer " + cfg.CircleAPIKey,
		}
		_, statusCode, err := fetchHTTP(circleURL, headers, nil)
		if err == nil {
			fmt.Printf("   ✅ Circle W3S API Status: %d OK\n", statusCode)
		} else {
			fmt.Printf("   ⚠️ Circle API query note: %v\n", err)
		}
	}

	fmt.Println()
	fmt.Println("==================================================================")
	fmt.Println("                    TEST EXECUTION COMPLETE                       ")
	fmt.Println("==================================================================")
}
