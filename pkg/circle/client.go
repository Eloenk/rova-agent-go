package circle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	if c.Config.MockMode || c.Config.CircleAPIKey == "" {
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

func (c *CircleClient) ExecuteContract(ctx context.Context, contractAddress string, functionSig string, params []interface{}) (string, error) {
	if c.Config.MockMode || c.Config.CircleAPIKey == "" {
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

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Config.CircleAPIKey)
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
