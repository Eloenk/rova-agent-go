package circle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"rova-agent-go/pkg/config"
)

type CircleClient struct {
	Config     *config.Config
	HTTPClient *http.Client
}

func NewCircleClient(cfg *config.Config) *CircleClient {
	return &CircleClient{
		Config: cfg,
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
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
	IdempotencyKey  string   `json:"idempotencyKey"`
	WalletID        string   `json:"walletId"`
	ContractAddress string   `json:"contractAddress"`
	ABIFunctionSignature string `json:"abiFunctionSignature"`
	ABIParameters   []interface{} `json:"abiParameters"`
	FeeLevel        string   `json:"feeLevel"`
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
	targetWalletID := walletID
	if targetWalletID == "" {
		targetWalletID = c.Config.CircleWalletID
	}

	if c.Config.CircleAPIKey == "" || targetWalletID == "" {
		return "", fmt.Errorf("Circle API key and Wallet ID are required for live USDC transfer")
	}

	url := "https://api.circle.com/v1/w3s/developer/transactions/transfer"
	payload := TransferRequest{
		IdempotencyKey:  fmt.Sprintf("tx-%d", time.Now().UnixNano()),
		WalletID:        targetWalletID,
		DestinationAddr: recipient,
		Amount:          []string{fmt.Sprintf("%.6f", amount)},
		TokenID:         c.Config.USDCContractAddress,
		FeeLevel:        "MEDIUM",
	}

	return c.postTransaction(ctx, url, payload)
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
		strategy = "circle_agent_stack"
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

func (c *CircleClient) SwapStablecoins(ctx context.Context, walletAddress string, buyCurrency string, amount float64) (string, error) {
	return c.SwapStablecoinsWithWallet(ctx, c.Config.CircleWalletID, walletAddress, buyCurrency, amount)
}

func (c *CircleClient) SwapStablecoinsWithWallet(ctx context.Context, walletID string, walletAddress string, buyCurrency string, amount float64) (string, error) {
	sellCurrency := "USDC"
	if strings.ToUpper(buyCurrency) == "USDC" {
		sellCurrency = "EURC"
	}

	quote := c.GetSwapQuote(sellCurrency, buyCurrency, amount)
	fmt.Printf("[CircleAgentStackCLI] Executing swap via Circle Agent Stack CLI: %.2f %s -> %s (Rate: %.4f)\n",
		amount, sellCurrency, buyCurrency, quote.ExchangeRate)

	// Invoke Circle Agent Stack CLI (@circle-fin/cli) directly
	cmd := exec.CommandContext(ctx, "npx", "-y", "@circle-fin/cli", "swap",
		"--from", sellCurrency,
		"--to", buyCurrency,
		"--amount", fmt.Sprintf("%.6f", amount),
		"--wallet", walletID,
		"--output", "json",
	)

	cmd.Env = append(os.Environ(),
		fmt.Sprintf("CIRCLE_API_KEY=%s", c.Config.CircleAPIKey),
		fmt.Sprintf("CIRCLE_ENTITY_SECRET=%s", c.Config.CircleEntitySecret),
	)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		// If CLI is not present or requires interactive auth, fallback to Circle Developer-Controlled Wallet API execution
		sellToken := "0x3600000000000000000000000000000000000000" // USDC Address on Arc
		buyToken := "0x360000000000000000000000000000000001"  // EURC Address on Arc
		if strings.ToUpper(buyCurrency) == "USDC" {
			sellToken, buyToken = buyToken, sellToken
		}
		amountInt := int64(amount * 1e6)
		minOutInt := int64(float64(amountInt) * 0.90)
		deadline := time.Now().Add(20 * time.Minute).Unix()

		params := []interface{}{
			fmt.Sprintf("%d", amountInt),
			fmt.Sprintf("%d", minOutInt),
			[]string{sellToken, buyToken},
			walletAddress,
			fmt.Sprintf("%d", deadline),
		}

		routerAddr := c.Config.SwapRouterAddress
		if routerAddr == "" {
			routerAddr = "0x8FE6B999Dc680CcFDD5Bf7EB0974218be2542DAA"
		}
		return c.ExecuteContractWithWallet(ctx, walletID, routerAddr, "swapExactTokensForTokens(uint256,uint256,address[],address,uint256)", params)
	}

	var resStruct struct {
		TxHash string `json:"txHash"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(outBuf.Bytes(), &resStruct); err == nil && resStruct.TxHash != "" {
		return resStruct.TxHash, nil
	}

	return fmt.Sprintf("cli-exec-%d", time.Now().UnixNano()), nil
}

func (c *CircleClient) BridgeCCTP(ctx context.Context, walletAddress string, toChain string, amount float64) (string, error) {
	return c.BridgeCCTPWithWallet(ctx, c.Config.CircleWalletID, walletAddress, toChain, amount)
}

func (c *CircleClient) BridgeCCTPWithWallet(ctx context.Context, walletID string, walletAddress string, toChain string, amount float64) (string, error) {
	if c.Config.CircleAPIKey == "" {
		return "", fmt.Errorf("Circle API key is required for live bridge execution")
	}

	domain := "0" // Ethereum Sepolia
	if strings.Contains(strings.ToLower(toChain), "base") {
		domain = "6" // Base Sepolia
	}

	recipientBytes32 := fmt.Sprintf("0x000000000000000000000000%s", strings.TrimPrefix(walletAddress, "0x"))
	amountInt := int64(amount * 1e6)
	params := []interface{}{domain, recipientBytes32, fmt.Sprintf("%d", amountInt), "0x3600000000000000000000000000000000000000"}

	return c.ExecuteContractWithWallet(ctx, walletID, "0x9f3b8679c73c2Fef8b59B4f3444d4e156fb70AA5", "depositForBurn(uint64,bytes32,uint256,address)", params)
}

func (c *CircleClient) DepositSavingsVault(ctx context.Context, userWallet string, savingsSubWallet string, amount float64) (string, error) {
	if c.Config.CircleAPIKey == "" {
		return "", fmt.Errorf("Circle API key is required for savings vault deposit")
	}

	strategy := c.Config.VaultStrategy
	if strategy == "smart_contract" {
		vaultAddr := os.Getenv("ROVA_SAVINGS_VAULT_ADDRESS")
		if vaultAddr != "" {
			tokenAddr := "0x3600000000000000000000000000000000000000"
			amountInt := int64(amount * 1e6)
			lockDuration := int64(30 * 86400)

			_, _ = c.ExecuteContract(ctx, tokenAddr, "approve(address,uint256)", []interface{}{vaultAddr, fmt.Sprintf("%d", amountInt)})
			params := []interface{}{tokenAddr, fmt.Sprintf("%d", amountInt), fmt.Sprintf("%d", lockDuration)}
			return c.ExecuteContract(ctx, vaultAddr, "depositSavings(address,uint256,uint256)", params)
		}
	}

	targetWallet := savingsSubWallet
	if targetWallet == "" {
		targetWallet = userWallet
	}
	return c.TransferUSDC(ctx, targetWallet, amount)
}

func (c *CircleClient) ExecuteContract(ctx context.Context, contractAddress string, functionSig string, params []interface{}) (string, error) {
	return c.ExecuteContractWithWallet(ctx, c.Config.CircleWalletID, contractAddress, functionSig, params)
}

func (c *CircleClient) ExecuteContractWithWallet(ctx context.Context, walletID string, contractAddress string, functionSig string, params []interface{}) (string, error) {
	targetWalletID := walletID
	if targetWalletID == "" {
		targetWalletID = c.Config.CircleWalletID
	}

	if c.Config.CircleAPIKey == "" || targetWalletID == "" {
		return "", fmt.Errorf("Circle API key and Wallet ID are required for live contract execution")
	}

	url := "https://api.circle.com/v1/w3s/developer/transactions/contractExecution"
	payload := ContractExecutionRequest{
		IdempotencyKey:       fmt.Sprintf("exec-%d", time.Now().UnixNano()),
		WalletID:             targetWalletID,
		ContractAddress:      contractAddress,
		ABIFunctionSignature: functionSig,
		ABIParameters:        params,
		FeeLevel:             "MEDIUM",
	}

	return c.postTransaction(ctx, url, payload)
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

	apiKey := c.Config.CircleAPIKey
	if len(strings.Split(apiKey, ":")) == 2 {
		apiKey = "TEST_API_KEY:" + apiKey
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-User-Token", c.Config.CircleEntitySecret)

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

	if res.Data.TxHash != "" {
		return res.Data.TxHash, nil
	}
	return res.Data.ID, nil
}
