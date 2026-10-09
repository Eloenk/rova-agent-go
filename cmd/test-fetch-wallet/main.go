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
	"os/exec"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"rova-agent-go/pkg/config"
)

type UserRecord struct {
	ID                  string `json:"id"`
	Email               string `json:"email"`
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
		body, err := io.ReadAll(resp.Body)
		return body, resp.StatusCode, err
	}

	// Windows process socket fallback via system curl.exe
	if strings.Contains(err.Error(), "connectex") || strings.Contains(err.Error(), "forbidden") {
		args := []string{"-s", urlStr}
		if len(postBody) > 0 {
			args = append(args, "-X", "POST", "-d", string(postBody))
		}
		for k, v := range headers {
			args = append(args, "-H", fmt.Sprintf("%s: %s", k, v))
		}
		cmd := exec.Command("curl.exe", args...)
		output, cmdErr := cmd.Output()
		if cmdErr == nil && len(output) > 0 {
			return output, 200, nil
		}
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
	envFiles := []string{
		".env.local",
		".env",
		"../.env.local",
		"../.env",
		"../rova/.env.local",
		"../../rova/.env.local",
	}
	for _, file := range envFiles {
		_ = godotenv.Overload(file)
	}

	emailFlag := flag.String("email", "", "User email address to fetch Circle wallet for")
	flag.Parse()

	cfg := config.LoadConfig()

	fmt.Println("==================================================================")
	fmt.Println("         ROVA AGENT GO — CIRCLE WALLET FETCH TEST                 ")
	fmt.Println("==================================================================")

	targetEmail := strings.TrimSpace(strings.ToLower(*emailFlag))

	// 1. Query Supabase for User's Circle Wallet Address
	var circleWallet string
	var foundUser *UserRecord

	if cfg.SupabaseURL != "" && cfg.SupabaseServiceRoleKey != "" {
		fmt.Printf("🔍 1. Querying Supabase Database (%s)...\n", cfg.SupabaseURL)
		
		queryURL := fmt.Sprintf("%s/rest/v1/users?select=id,email,circle_wallet_address", cfg.SupabaseURL)
		if targetEmail != "" {
			queryURL += fmt.Sprintf("&email=eq.%s", url.QueryEscape(targetEmail))
		} else {
			queryURL += "&limit=5"
		}

		headers := map[string]string{
			"apikey":        cfg.SupabaseServiceRoleKey,
			"Authorization": "Bearer " + cfg.SupabaseServiceRoleKey,
		}

		bodyBytes, statusCode, err := fetchHTTP(queryURL, headers, nil)
		if err == nil && statusCode == 200 {
			var users []UserRecord
			if err := json.Unmarshal(bodyBytes, &users); err == nil && len(users) > 0 {
				foundUser = &users[0]
				circleWallet = foundUser.CircleWalletAddress
				fmt.Printf("   ✅ Supabase User Found:   %s\n", foundUser.Email)
				fmt.Printf("   💳 Circle Wallet Address:  %s\n", circleWallet)
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
		fmt.Printf("   🌐 RPC Endpoint: %s\n", cfg.ArcRPCURL)

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
