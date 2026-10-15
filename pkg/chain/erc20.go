package chain

import (
	"context"
	"fmt"
	"math"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func (c *ChainClient) TransferUSDC(ctx context.Context, recipient string, amountUsdc float64) (string, error) {
	if c.Config.ExecutionMode == "circle" {
		return c.CircleClient.TransferUSDC(ctx, recipient, amountUsdc)
	}
	return c.TransferUSDCFromWallet(ctx, "", recipient, amountUsdc)
}

func (c *ChainClient) TransferUSDCFromWallet(ctx context.Context, sourceWallet, recipient string, amountUsdc float64) (string, error) {
	// Mode: Circle Developer Controlled Wallets
	if c.Config.ExecutionMode == "circle" {
		if !common.IsHexAddress(sourceWallet) {
			return "", fmt.Errorf("source wallet must be a valid EVM address")
		}
		return c.CircleClient.TransferUSDCFromWallet(ctx, sourceWallet, recipient, amountUsdc)
	}

	// Mode: Direct RPC Transfer
	if !common.IsHexAddress(recipient) || !common.IsHexAddress(c.Config.USDCContractAddress) {
		return "", fmt.Errorf("recipient and USDC contract must be valid EVM addresses")
	}
	if sourceWallet != "" && (!common.IsHexAddress(sourceWallet) || common.HexToAddress(sourceWallet) != c.Address) {
		return "", fmt.Errorf("source wallet does not match the configured direct-signing key")
	}
	amountInt, err := c.scaledUSDCAmount(amountUsdc)
	if err != nil {
		return "", err
	}

	toAddr := common.HexToAddress(recipient)
	usdcContract := common.HexToAddress(c.Config.USDCContractAddress)

	amountBig := new(big.Int).SetInt64(amountInt)
	methodID := crypto.Keccak256([]byte("transfer(address,uint256)"))[:4]

	paddedAddress := common.LeftPadBytes(toAddr.Bytes(), 32)
	paddedAmount := common.LeftPadBytes(amountBig.Bytes(), 32)

	var data []byte
	data = append(data, methodID...)
	data = append(data, paddedAddress...)
	data = append(data, paddedAmount...)

	return c.SignAndSendTx(ctx, usdcContract, big.NewInt(0), data)
}

func (c *ChainClient) GetUSDCBalance(ctx context.Context, target string) (float64, error) {
	if c.RPCClient == nil {
		return c.GetBalanceUSDCWithFailover(ctx, target)
	}

	if !common.IsHexAddress(target) || !common.IsHexAddress(c.Config.USDCContractAddress) {
		return 0, fmt.Errorf("wallet and USDC contract must be valid EVM addresses")
	}
	targetAddr := common.HexToAddress(target)
	usdcContract := common.HexToAddress(c.Config.USDCContractAddress)

	methodID := crypto.Keccak256([]byte("balanceOf(address)"))[:4]
	paddedAddress := common.LeftPadBytes(targetAddr.Bytes(), 32)

	var data []byte
	data = append(data, methodID...)
	data = append(data, paddedAddress...)

	msg := ethereum.CallMsg{
		To:   &usdcContract,
		Data: data,
	}

	res, err := c.RPCClient.CallContract(ctx, msg, nil)
	if err != nil {
		return 0, fmt.Errorf("balanceOf call failed: %w", err)
	}

	balanceBig := new(big.Int).SetBytes(res)
	balanceFloat := new(big.Float).SetInt(balanceBig)
	usdcVal, _ := new(big.Float).Quo(balanceFloat, big.NewFloat(1e6)).Float64()

	return usdcVal, nil
}

func (c *ChainClient) GetEURCBalance(ctx context.Context, target string) (float64, error) {
	if c.RPCClient == nil {
		return c.GetBalanceEURCWithFailover(ctx, target)
	}

	eurcAddr := c.Config.EURCContractAddress
	if !common.IsHexAddress(target) || !common.IsHexAddress(eurcAddr) {
		return 0, fmt.Errorf("wallet and EURC contract must be valid EVM addresses")
	}

	targetAddr := common.HexToAddress(target)
	eurcContract := common.HexToAddress(eurcAddr)

	methodID := crypto.Keccak256([]byte("balanceOf(address)"))[:4]
	paddedAddress := common.LeftPadBytes(targetAddr.Bytes(), 32)

	var data []byte
	data = append(data, methodID...)
	data = append(data, paddedAddress...)

	msg := ethereum.CallMsg{
		To:   &eurcContract,
		Data: data,
	}

	res, err := c.RPCClient.CallContract(ctx, msg, nil)
	if err != nil {
		return 0, fmt.Errorf("balanceOf call failed: %w", err)
	}

	balanceBig := new(big.Int).SetBytes(res)
	balanceFloat := new(big.Float).SetInt(balanceBig)
	eurcVal, _ := new(big.Float).Quo(balanceFloat, big.NewFloat(1e6)).Float64()

	return eurcVal, nil
}

func (c *ChainClient) scaledUSDCAmount(amount float64) (int64, error) {
	maximum := math.Max(c.Config.MaxAutonomousAmount, c.Config.MaxWhatsAppActionAmount)
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 || amount > maximum {
		return 0, fmt.Errorf("amount must be greater than zero and no more than %.2f", maximum)
	}
	scaled := math.Round(amount * 1e6)
	if math.Abs(amount*1e6-scaled) > 1e-6 {
		return 0, fmt.Errorf("amount supports at most six decimal places")
	}
	return int64(scaled), nil
}
