package chain

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func (c *ChainClient) TransferUSDC(ctx context.Context, recipient string, amountUsdc float64) (string, error) {
	// Mode: Circle Developer Controlled Wallets
	if c.Config.ExecutionMode == "circle" {
		return c.CircleClient.TransferUSDC(ctx, recipient, amountUsdc)
	}

	// Mode: Direct RPC Transfer
	toAddr := common.HexToAddress(recipient)
	usdcContract := common.HexToAddress(c.Config.USDCContractAddress)

	amountBig := new(big.Int).SetInt64(int64(amountUsdc * 1e6))
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
