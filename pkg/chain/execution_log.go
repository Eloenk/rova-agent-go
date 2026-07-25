package chain

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type LogExecutionOpts struct {
	RuleID          string
	Recipient       string
	AmountUsdc      float64
	RateAtExecution float64
	Memo            string
}

func (c *ChainClient) LogExecutionOnchain(ctx context.Context, opts LogExecutionOpts) (string, error) {
	contractAddr := c.Config.ExecutionLogContractAddress
	recipientAddr := opts.Recipient

	ruleHash := crypto.Keccak256([]byte(opts.RuleID))
	var ruleIDBytes32 [32]byte
	copy(ruleIDBytes32[:], ruleHash)

	amountUsdc6 := fmt.Sprintf("%d", int64(opts.AmountUsdc*1e6))
	rate1e6 := fmt.Sprintf("%d", int64(opts.RateAtExecution*1e6))

	// Mode: Circle Developer Controlled Wallets
	if c.Config.ExecutionMode == "circle" {
		params := []interface{}{
			fmt.Sprintf("0x%x", ruleIDBytes32),
			recipientAddr,
			amountUsdc6,
			rate1e6,
			opts.Memo,
		}
		return c.CircleClient.ExecuteContract(ctx, contractAddr, "logExecution(bytes32,address,uint256,uint256,string)", params)
	}

	// Mode: Direct RPC Execution
	contractCommonAddr := common.HexToAddress(contractAddr)
	recipientCommonAddr := common.HexToAddress(recipientAddr)

	amountBig := big.NewInt(int64(opts.AmountUsdc * 1e6))
	rateBig := big.NewInt(int64(opts.RateAtExecution * 1e6))

	methodID := crypto.Keccak256([]byte("logExecution(bytes32,address,uint256,uint256,string)"))[:4]

	paddedRuleID := ruleIDBytes32[:]
	paddedRecipient := common.LeftPadBytes(recipientCommonAddr.Bytes(), 32)
	paddedAmount := common.LeftPadBytes(amountBig.Bytes(), 32)
	paddedRate := common.LeftPadBytes(rateBig.Bytes(), 32)

	memoOffset := common.LeftPadBytes(big.NewInt(160).Bytes(), 32)

	memoBytes := []byte(opts.Memo)
	memoLen := common.LeftPadBytes(big.NewInt(int64(len(memoBytes))).Bytes(), 32)

	memoPaddedLen := ((len(memoBytes) + 31) / 32) * 32
	memoPadded := make([]byte, memoPaddedLen)
	copy(memoPadded, memoBytes)

	var data []byte
	data = append(data, methodID...)
	data = append(data, paddedRuleID...)
	data = append(data, paddedRecipient...)
	data = append(data, paddedAmount...)
	data = append(data, paddedRate...)
	data = append(data, memoOffset...)
	data = append(data, memoLen...)
	data = append(data, memoPadded...)

	txHash, err := c.SignAndSendTx(ctx, contractCommonAddr, big.NewInt(0), data)
	if err != nil {
		return "", fmt.Errorf("failed to call logExecution onchain: %w", err)
	}

	return txHash, nil
}
