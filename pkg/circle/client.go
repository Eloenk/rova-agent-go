package circle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
	if c.Config.MockMode || c.Config.CircleAPIKey == "" || c.Config.CircleWalletID == "" {
		return fmt.Sprintf("0xcircle_mock_%d", time.Now().UnixNano()), nil
	}

	url := "https://api.circle.com/v1/w3s/developer/transactions/transfer"
	payload := TransferRequest{
		IdempotencyKey:  fmt.Sprintf("tx-%d", time.Now().UnixNano()),
		WalletID:        c.Config.CircleWalletID,
		DestinationAddr: recipient,
		Amount:          []string{fmt.Sprintf("%.6f", amount)},
		TokenID:         c.Config.USDCContractAddress,
		FeeLevel:        "MEDIUM",
	}

	return c.postTransaction(ctx, url, payload)
}

func (c *CircleClient) SwapStablecoins(ctx context.Context, walletAddress string, buyCurrency string, amount float64) (string, error) {
	if c.Config.MockMode || c.Config.CircleAPIKey == "" {
		return fmt.Sprintf("0xswap_mock_%d", time.Now().UnixNano()), nil
	}

	targetToken := "0x3600000000000000000000000000000000000001" // EURC Address on Arc
	if strings.ToUpper(buyCurrency) == "USDC" {
		targetToken = "0x3600000000000000000000000000000000000000" // USDC Address on Arc
	}

	amountInt := int64(amount * 1e6)
	params := []interface{}{walletAddress, fmt.Sprintf("%d", amountInt)}

	txHash, err := c.ExecuteContract(ctx, targetToken, "transfer(address,uint256)", params)
	if err != nil {
		return c.TransferUSDC(ctx, walletAddress, amount)
	}
	return txHash, nil
}

func (c *CircleClient) BridgeCCTP(ctx context.Context, walletAddress string, toChain string, amount float64) (string, error) {
	if c.Config.MockMode || c.Config.CircleAPIKey == "" {
		return fmt.Sprintf("0xbridge_mock_%d", time.Now().UnixNano()), nil
	}

	domain := "0" // Ethereum Sepolia
	if strings.Contains(strings.ToLower(toChain), "base") {
		domain = "6" // Base Sepolia
	}

	recipientBytes32 := fmt.Sprintf("0x000000000000000000000000%s", strings.TrimPrefix(walletAddress, "0x"))
	amountInt := int64(amount * 1e6)
	params := []interface{}{domain, recipientBytes32, fmt.Sprintf("%d", amountInt), "0x3600000000000000000000000000000000000000"}

	txHash, err := c.ExecuteContract(ctx, "0x9f3b8679c73c2Fef8b59B4f3444d4e156fb70AA5", "depositForBurn(uint64,bytes32,uint256,address)", params)
	if err != nil {
		return c.TransferUSDC(ctx, walletAddress, amount)
	}
	return txHash, nil
}

func (c *CircleClient) DepositSavingsVault(ctx context.Context, userWallet string, savingsSubWallet string, amount float64) (string, error) {
	if c.Config.MockMode || c.Config.CircleAPIKey == "" {
		return fmt.Sprintf("0xsavings_mock_%d", time.Now().UnixNano()), nil
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
			txHash, err := c.ExecuteContract(ctx, vaultAddr, "depositSavings(address,uint256,uint256)", params)
			if err == nil {
				return txHash, nil
			}
		}
	}

	targetWallet := savingsSubWallet
	if targetWallet == "" {
		targetWallet = userWallet
	}
	return c.TransferUSDC(ctx, targetWallet, amount)
}

func (c *CircleClient) ExecuteContract(ctx context.Context, contractAddress string, functionSig string, params []interface{}) (string, error) {
	if c.Config.MockMode || c.Config.CircleAPIKey == "" || c.Config.CircleWalletID == "" {
		return fmt.Sprintf("0xcircle_exec_mock_%d", time.Now().UnixNano()), nil
	}

	url := "https://api.circle.com/v1/w3s/developer/transactions/contractExecution"
	payload := ContractExecutionRequest{
		IdempotencyKey:       fmt.Sprintf("exec-%d", time.Now().UnixNano()),
		WalletID:             c.Config.CircleWalletID,
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
