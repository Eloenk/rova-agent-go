package chain

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type LogExecutionOpts struct {
	RuleID          string
	SourceWallet    string
	Recipient       string
	AmountUsdc      float64
	RateAtExecution float64
	Memo            string
}

func (c *ChainClient) LogExecutionOnchain(ctx context.Context, opts LogExecutionOpts) (string, error) {
	contractAddr := c.Config.ExecutionLogContractAddress
	recipientAddr := opts.Recipient
	if !common.IsHexAddress(contractAddr) || !common.IsHexAddress(recipientAddr) {
		return "", fmt.Errorf("execution-log contract and recipient must be valid EVM addresses")
	}
	amountInt, err := c.scaledUSDCAmount(opts.AmountUsdc)
	if err != nil {
		return "", err
	}
	if math.IsNaN(opts.RateAtExecution) || math.IsInf(opts.RateAtExecution, 0) || opts.RateAtExecution <= 0 || opts.RateAtExecution > 1_000_000 {
		return "", fmt.Errorf("execution rate is invalid")
	}
	memo := strings.TrimSpace(opts.Memo)
	if memo == "" || len([]byte(memo)) > 256 {
		return "", fmt.Errorf("execution memo must contain 1 to 256 bytes")
	}

	ruleHash := crypto.Keccak256([]byte(opts.RuleID))
	var ruleIDBytes32 [32]byte
	copy(ruleIDBytes32[:], ruleHash)

	amountUsdc6 := fmt.Sprintf("%d", amountInt)
	rateInt := int64(math.Round(opts.RateAtExecution * 1e6))
	rate1e6 := fmt.Sprintf("%d", rateInt)

	// Mode: Circle Developer Controlled Wallets
	if c.Config.ExecutionMode == "circle" {
		if !common.IsHexAddress(opts.SourceWallet) {
			return "", fmt.Errorf("source wallet must be a valid EVM address")
		}
		params := []interface{}{
			fmt.Sprintf("0x%x", ruleIDBytes32),
			recipientAddr,
			amountUsdc6,
			rate1e6,
			memo,
		}
		return c.CircleClient.ExecuteContractWithWallet(ctx, opts.SourceWallet, contractAddr, "logExecution(bytes32,address,uint256,uint256,string)", params)
	}

	// Mode: Direct RPC Execution
	if opts.SourceWallet != "" && (!common.IsHexAddress(opts.SourceWallet) || common.HexToAddress(opts.SourceWallet) != c.Address) {
		return "", fmt.Errorf("source wallet does not match the configured direct-signing key")
	}
	contractCommonAddr := common.HexToAddress(contractAddr)
	recipientCommonAddr := common.HexToAddress(recipientAddr)

	amountBig := big.NewInt(amountInt)
	rateBig := big.NewInt(rateInt)

	methodID := crypto.Keccak256([]byte("logExecution(bytes32,address,uint256,uint256,string)"))[:4]

	paddedRuleID := ruleIDBytes32[:]
	paddedRecipient := common.LeftPadBytes(recipientCommonAddr.Bytes(), 32)
	paddedAmount := common.LeftPadBytes(amountBig.Bytes(), 32)
	paddedRate := common.LeftPadBytes(rateBig.Bytes(), 32)

	memoOffset := common.LeftPadBytes(big.NewInt(160).Bytes(), 32)

	memoBytes := []byte(memo)
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
