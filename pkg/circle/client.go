package circle

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"rova-agent-go/pkg/config"
)

type CircleClient struct {
	Config     *config.Config
	HTTPClient *http.Client

	// Cached Circle entity public key for RSA-OAEP encryption
	pubKeyOnce sync.Once
	pubKey     *rsa.PublicKey
	pubKeyErr  error
}

func NewCircleClient(cfg *config.Config) *CircleClient {
	return &CircleClient{
		Config: cfg,
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// fetchEntityPublicKey retrieves Circle's RSA public key for entity secret encryption.
// Includes retry loop to handle firewall delays / transient network blocks.
func (c *CircleClient) fetchEntityPublicKey() (*rsa.PublicKey, error) {
	if c.pubKey != nil {
		return c.pubKey, nil
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequest("GET", "https://api.circle.com/v1/w3s/config/entity/publicKey", nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create public key request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.Config.CircleAPIKey)

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("failed to fetch Circle entity public key (attempt %d/3): %w", attempt, err)
			log.Printf("[Circle] %v - retrying in 1s...", lastErr)
			time.Sleep(1 * time.Second)
			continue
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()

		if err != nil {
			lastErr = fmt.Errorf("failed to read Circle public key response body: %w", err)
			time.Sleep(1 * time.Second)
			continue
		}

		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("Circle public key API error (%d): %s", resp.StatusCode, string(bodyBytes))
		}

		var result struct {
			Data struct {
				PublicKey string `json:"publicKey"`
			} `json:"data"`
		}
		if err := json.Unmarshal(bodyBytes, &result); err != nil {
			return nil, fmt.Errorf("failed to decode Circle public key response: %w", err)
		}

		block, _ := pem.Decode([]byte(result.Data.PublicKey))
		if block == nil {
			return nil, fmt.Errorf("failed to PEM-decode Circle entity public key")
		}

		pubInterface, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse Circle public key: %w", err)
		}

		rsaPub, ok := pubInterface.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("Circle public key is not RSA")
		}

		c.pubKey = rsaPub
		log.Println("[Circle] Entity public key fetched and cached successfully")
		return c.pubKey, nil
	}

	return nil, lastErr
}

// generateEntitySecretCiphertext encrypts the entity secret with RSA-OAEP SHA-256
// and returns a base64-encoded ciphertext string. Must be generated fresh per request.
func (c *CircleClient) generateEntitySecretCiphertext() (string, error) {
	pubKey, err := c.fetchEntityPublicKey()
	if err != nil {
		return "", err
	}

	entitySecretHex := c.Config.CircleEntitySecret
	if entitySecretHex == "" {
		return "", fmt.Errorf("CIRCLE_ENTITY_SECRET is not configured")
	}

	// Circle expects the 32 raw decoded bytes of the 64-char hex entity secret to be encrypted
	rawBytes, err := hex.DecodeString(strings.TrimSpace(entitySecretHex))
	if err != nil {
		return "", fmt.Errorf("failed to hex-decode CIRCLE_ENTITY_SECRET: %w", err)
	}

	hash := sha256.New()
	ciphertext, err := rsa.EncryptOAEP(hash, rand.Reader, pubKey, rawBytes, nil)
	if err != nil {
		return "", fmt.Errorf("RSA-OAEP encryption of entity secret failed: %w", err)
	}

	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

type TransferRequest struct {
	IdempotencyKey  string   `json:"idempotencyKey"`
	WalletID        string   `json:"walletId"`
	DestinationAddr string   `json:"destinationAddress"`
	Amount          []string `json:"amounts"`
	TokenID         string   `json:"tokenId"`
	FeeLevel        string   `json:"feeLevel"`
}

type ContractExecutionRequest struct {
	IdempotencyKey         string        `json:"idempotencyKey"`
	EntitySecretCiphertext string        `json:"entitySecretCiphertext"`
	WalletID               string        `json:"walletId,omitempty"`
	WalletAddress          string        `json:"walletAddress,omitempty"`
	Blockchain             string        `json:"blockchain,omitempty"`
	ContractAddress        string        `json:"contractAddress"`
	ABIFunctionSignature   string        `json:"abiFunctionSignature"`
	ABIParameters          []interface{} `json:"abiParameters"`
	FeeLevel               string        `json:"feeLevel"`
}

type CircleTxResponse struct {
	Data struct {
		ID        string `json:"id"`
		State     string `json:"state"`
		TxHash    string `json:"txHash"`
		ErrorReason string `json:"errorReason,omitempty"`
	} `json:"data"`
}

func (c *CircleClient) TransferUSDC(ctx context.Context, recipient string, amount float64) (string, error) {
	return c.TransferUSDCFromWallet(ctx, c.Config.CircleWalletID, recipient, amount)
}

func (c *CircleClient) TransferUSDCFromWallet(ctx context.Context, walletID string, recipient string, amount float64) (string, error) {
	if err := c.requireExecutionEnabled(); err != nil {
		return "", err
	}

	targetWalletID := walletID
	if targetWalletID == "" {
		targetWalletID = c.Config.CircleWalletID
	}

	if c.Config.CircleAPIKey == "" || targetWalletID == "" {
		return "", fmt.Errorf("Circle API key and Wallet ID are required for live USDC transfer")
	}

	usdcContract := c.Config.USDCContractAddress

	amountInt := int64(amount * 1e6)
	params := []interface{}{recipient, fmt.Sprintf("%d", amountInt)}

	return c.ExecuteContractWithWallet(ctx, targetWalletID, usdcContract, "transfer(address,uint256)", params)
}

type SwapQuote struct {
	SellCurrency       string  `json:"sellCurrency"`
	BuyCurrency        string  `json:"buyCurrency"`
	SellAmount         float64 `json:"sellAmount"`
	EstimatedBuyAmount float64 `json:"estimatedBuyAmount"`
	ExchangeRate       float64 `json:"exchangeRate"`
	Strategy           string  `json:"strategy"`
}

func (c *CircleClient) GetSwapQuote(sellCurrency, buyCurrency string, amount float64) *SwapQuote {
	rate := 0.92
	if strings.ToUpper(buyCurrency) == "USDC" {
		rate = 1.087
	}
	estBuy := amount * rate
	strategy := c.Config.SwapStrategy
	if strategy == "" {
		strategy = "sidecar_uds"
	}

	return &SwapQuote{
		SellCurrency:       sellCurrency,
		BuyCurrency:        buyCurrency,
		SellAmount:         amount,
		EstimatedBuyAmount: estBuy,
		ExchangeRate:       rate,
		Strategy:           strategy,
	}
}

func (c *CircleClient) GetWalletAddress(ctx context.Context, walletID string) (string, error) {
	if strings.HasPrefix(walletID, "0x") {
		return walletID, nil
	}
	url := fmt.Sprintf("https://api.circle.com/v1/w3s/wallets/%s", walletID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Config.CircleAPIKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			Wallet struct {
				Address string `json:"address"`
			} `json:"wallet"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && result.Data.Wallet.Address != "" {
		return result.Data.Wallet.Address, nil
	}

	return "", fmt.Errorf("failed to resolve wallet address for ID %s", walletID)
}

func (c *CircleClient) SwapStablecoins(ctx context.Context, walletAddress string, buyCurrency string, amount float64) (string, error) {
	return c.SwapStablecoinsWithWallet(ctx, c.Config.CircleWalletID, walletAddress, buyCurrency, amount)
}

type SwapSidecarRequest struct {
	WalletAddress string  `json:"walletAddress"`
	SellCurrency  string  `json:"sellCurrency"`
	BuyCurrency   string  `json:"buyCurrency"`
	Amount        float64 `json:"amount"`
}

type SwapSidecarResponse struct {
	OK     bool   `json:"ok"`
	TxHash string `json:"txHash"`
	Error  string `json:"error"`
}

func (c *CircleClient) SwapViaUDSSidecar(ctx context.Context, walletAddress, sellCurrency, buyCurrency string, amount float64) (string, error) {
	if err := c.requireExecutionEnabled(); err != nil {
		return "", err
	}
	if len(c.Config.SwapSidecarToken) < 32 {
		return "", fmt.Errorf("ROVA_SIDECAR_TOKEN must be configured with at least 32 characters")
	}

	socketPath := c.Config.SwapSidecarSocket
	if socketPath == "" {
		socketPath = "/tmp/rova-swap.sock"
	}

	reqPayload := SwapSidecarRequest{
		WalletAddress: walletAddress,
		SellCurrency:  sellCurrency,
		BuyCurrency:   buyCurrency,
		Amount:        amount,
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal swap sidecar request: %w", err)
	}

	// Create UDS client or TCP fallback
	var client *http.Client
	var targetURL string

	if strings.HasPrefix(socketPath, "http://") || strings.HasPrefix(socketPath, "https://") {
		// TCP / HTTP Fallback
		client = &http.Client{Timeout: 30 * time.Second}
		targetURL = fmt.Sprintf("%s/api/swap", strings.TrimRight(socketPath, "/"))
	} else {
		// Unix Domain Socket (UDS) IPC
		client = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		}
		targetURL = "http://unix/api/swap"
	}

	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to build sidecar HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Config.SwapSidecarToken)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sidecar UDS request failed (%s): %w", socketPath, err)
	}
	defer resp.Body.Close()

	var res SwapSidecarResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", fmt.Errorf("failed to decode sidecar response: %w", err)
	}

	if !res.OK {
		return "", fmt.Errorf("swap sidecar returned error: %s", res.Error)
	}

	return res.TxHash, nil
}

func (c *CircleClient) SwapStablecoinsWithWallet(ctx context.Context, walletID string, walletAddress string, buyCurrency string, amount float64) (string, error) {
	sellCurrency := "USDC"
	if strings.ToUpper(buyCurrency) == "USDC" {
		sellCurrency = "EURC"
	}

	targetAddr := walletAddress
	if targetAddr == "" {
		if addr, err := c.GetWalletAddress(ctx, walletID); err == nil {
			targetAddr = addr
		} else {
			targetAddr = c.Config.CircleWalletID
		}
	}

	quote := c.GetSwapQuote(sellCurrency, buyCurrency, amount)
	log.Printf("[Sidecar UDS] Executing swap: %.2f %s -> %s for wallet %s (Est: %.4f)", amount, sellCurrency, buyCurrency, targetAddr, quote.EstimatedBuyAmount)

	return c.SwapViaUDSSidecar(ctx, targetAddr, sellCurrency, buyCurrency, amount)
}

func (c *CircleClient) BridgeCCTP(ctx context.Context, walletAddress string, toChain string, amount float64) (string, error) {
	return c.BridgeCCTPWithWallet(ctx, c.Config.CircleWalletID, walletAddress, toChain, amount)
}

func (c *CircleClient) BridgeCCTPWithWallet(ctx context.Context, walletID string, walletAddress string, toChain string, amount float64) (string, error) {
	if err := c.requireExecutionEnabled(); err != nil {
		return "", err
	}

	if c.Config.CircleAPIKey == "" {
		return "", fmt.Errorf("Circle API key is required for live bridge execution")
	}

	domain := "0" // Ethereum Sepolia
	if strings.Contains(strings.ToLower(toChain), "base") {
		domain = "6" // Base Sepolia
	}

	recipientBytes32 := fmt.Sprintf("0x000000000000000000000000%s", strings.TrimPrefix(walletAddress, "0x"))
	amountInt := int64(amount * 1e6)
	params := []interface{}{domain, recipientBytes32, fmt.Sprintf("%d", amountInt), c.Config.USDCContractAddress}

	return c.ExecuteContractWithWallet(ctx, walletID, "0x9f3b8679c73c2Fef8b59B4f3444d4e156fb70AA5", "depositForBurn(uint64,bytes32,uint256,address)", params)
}

func (c *CircleClient) DepositSavingsVault(ctx context.Context, userWallet string, savingsSubWallet string, amount float64) (string, error) {
	if err := c.requireExecutionEnabled(); err != nil {
		return "", err
	}

	if c.Config.CircleAPIKey == "" {
		return "", fmt.Errorf("Circle API key is required for savings vault deposit")
	}

	vaultAddr := os.Getenv("ROVA_SAVINGS_VAULT_ADDRESS")
	if vaultAddr == "" {
		vaultAddr = "0x9330DA5152Cc676a029cfaCCcA3948e11EDE9BfB"
	}

	tokenAddr := c.Config.USDCContractAddress
	if tokenAddr == "" {
		tokenAddr = "0x3600000000000000000000000000000000000000"
	}

	amountInt := int64(amount * 1e6)
	lockDuration := int64(30 * 86400) // Default 30-day timelock if unassigned

	execWallet := userWallet
	if execWallet == "" {
		execWallet = c.Config.CircleWalletID
	}

	log.Printf("[Savings Vault] Executing smart contract deposit: %.2f USDC into %s (Lock: 30 days)", amount, vaultAddr)

	// Step 1: Approve RovaSavingsVault contract to spend USDC
	_, err := c.ExecuteContractWithWallet(ctx, execWallet, tokenAddr, "approve(address,uint256)", []interface{}{vaultAddr, fmt.Sprintf("%d", amountInt)})
	if err != nil {
		return "", fmt.Errorf("failed to approve USDC for RovaSavingsVault: %w", err)
	}

	// Step 2: Deposit into RovaSavingsVault timelock contract
	params := []interface{}{tokenAddr, fmt.Sprintf("%d", amountInt), fmt.Sprintf("%d", lockDuration)}
	return c.ExecuteContractWithWallet(ctx, execWallet, vaultAddr, "depositSavings(address,uint256,uint256)", params)
}

func (c *CircleClient) RedeemSavingsVault(ctx context.Context, userWallet string, depositID int64) (string, error) {
	if err := c.requireExecutionEnabled(); err != nil {
		return "", err
	}

	if c.Config.CircleAPIKey == "" {
		return "", fmt.Errorf("Circle API key is required for savings vault redemption")
	}

	vaultAddr := os.Getenv("ROVA_SAVINGS_VAULT_ADDRESS")
	if vaultAddr == "" {
		vaultAddr = "0x9330DA5152Cc676a029cfaCCcA3948e11EDE9BfB"
	}

	execWallet := userWallet
	if execWallet == "" {
		execWallet = c.Config.CircleWalletID
	}

	log.Printf("[Savings Vault] Executing smart contract redeem: depositId #%d from %s", depositID, vaultAddr)

	return c.ExecuteContractWithWallet(ctx, execWallet, vaultAddr, "redeemSavings(uint256)", []interface{}{fmt.Sprintf("%d", depositID)})
}

func (c *CircleClient) ExecuteContract(ctx context.Context, contractAddress string, functionSig string, params []interface{}) (string, error) {
	return c.ExecuteContractWithWallet(ctx, c.Config.CircleWalletID, contractAddress, functionSig, params)
}

func (c *CircleClient) ExecuteContractWithWallet(ctx context.Context, walletID string, contractAddress string, functionSig string, params []interface{}) (string, error) {
	if err := c.requireExecutionEnabled(); err != nil {
		return "", err
	}

	targetWalletID := walletID
	if targetWalletID == "" {
		targetWalletID = c.Config.CircleWalletID
	}

	if c.Config.CircleAPIKey == "" || targetWalletID == "" {
		return "", fmt.Errorf("Circle API key and Wallet ID are required for live contract execution")
	}

	// Generate fresh entitySecretCiphertext for this request
	ciphertext, err := c.generateEntitySecretCiphertext()
	if err != nil {
		return "", fmt.Errorf("failed to generate entity secret ciphertext: %w", err)
	}

	url := "https://api.circle.com/v1/w3s/developer/transactions/contractExecution"
	payload := ContractExecutionRequest{
		IdempotencyKey:         uuid.New().String(),
		EntitySecretCiphertext: ciphertext,
		ContractAddress:        contractAddress,
		ABIFunctionSignature:   functionSig,
		ABIParameters:          params,
		FeeLevel:               "MEDIUM",
	}

	if strings.HasPrefix(targetWalletID, "0x") {
		payload.WalletAddress = targetWalletID
		payload.Blockchain = "ARC-TESTNET"
	} else {
		payload.WalletID = targetWalletID
	}

	return c.postTransaction(ctx, url, payload)
}

func (c *CircleClient) requireExecutionEnabled() error {
	if c.Config == nil || !c.Config.ExecutionEnabled {
		return fmt.Errorf("custodial execution is disabled; set ROVA_EXECUTION_ENABLED=true only after operational approval")
	}
	return nil
}

func (c *CircleClient) postTransaction(ctx context.Context, url string, payload interface{}) (string, error) {
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal Circle payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Config.CircleAPIKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Circle API request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("Circle API error (%d): %s", resp.StatusCode, string(bodyBytes))
	}

	var res CircleTxResponse
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return "", fmt.Errorf("failed to unmarshal Circle response: %w", err)
	}

	if res.Data.TxHash != "" && strings.HasPrefix(res.Data.TxHash, "0x") {
		return res.Data.TxHash, nil
	}

	txID := res.Data.ID
	if txID == "" {
		return "", fmt.Errorf("Circle API returned empty transaction ID")
	}

	// Poll until COMPLETE to retrieve true on-chain txHash starting with 0x (or error if state is FAILED / 422)
	return c.waitForTransactionCompletion(ctx, txID)
}

func (c *CircleClient) waitForTransactionCompletion(ctx context.Context, txID string) (string, error) {
	url := fmt.Sprintf("https://api.circle.com/v1/w3s/transactions/%s", txID)

	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(1 * time.Second):
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return "", fmt.Errorf("failed to create transaction status request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.Config.CircleAPIKey)

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			log.Printf("[Circle] Error polling transaction status: %v", err)
			continue
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode >= 400 {
			log.Printf("[Circle] Polling error (%d): %s", resp.StatusCode, string(bodyBytes))
			continue
		}

		var pollRes struct {
			Data struct {
				Transaction struct {
					ID          string `json:"id"`
					State       string `json:"state"`
					TxHash      string `json:"txHash"`
					ErrorReason string `json:"errorReason"`
				} `json:"transaction"`
			} `json:"data"`
		}

		if err := json.Unmarshal(bodyBytes, &pollRes); err == nil {
			tx := pollRes.Data.Transaction
			if tx.State == "COMPLETE" {
				if tx.TxHash != "" {
					return tx.TxHash, nil
				}
				return tx.ID, nil
			}
			if tx.State == "FAILED" || tx.State == "CANCELLED" || tx.State == "DENIED" {
				errReason := tx.ErrorReason
				if errReason == "" {
					errReason = "Transaction failed on Circle/Arc network"
				}
				return "", fmt.Errorf("Circle API transaction failed (State: %s): %s", tx.State, errReason)
			}
		}
	}

	return "", fmt.Errorf("Circle transaction %s timed out after 30 seconds waiting for on-chain confirmation", txID)
}
