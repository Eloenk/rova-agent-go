package chain

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"rova-agent-go/pkg/circle"
	"rova-agent-go/pkg/config"
)

type ChainClient struct {
	RPCClient    *ethclient.Client
	CircleClient *circle.CircleClient
	PrivateKey   *ecdsa.PrivateKey
	Address      common.Address
	ChainID      *big.Int
	Config       *config.Config
}

func NewChainClient(cfg *config.Config) (*ChainClient, error) {
	circleClient := circle.NewCircleClient(cfg)

	// Mode 1: Circle Managed Wallets
	if cfg.ExecutionMode == "circle" {
		var addr common.Address
		if cfg.CircleWalletID != "" && len(cfg.CircleWalletID) >= 42 && strings.HasPrefix(cfg.CircleWalletID, "0x") {
			addr = common.HexToAddress(cfg.CircleWalletID)
		} else {
			addr = common.HexToAddress("0x210c024beeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
		}
		return &ChainClient{
			CircleClient: circleClient,
			Address:      addr,
			ChainID:      big.NewInt(cfg.ChainID),
			Config:       cfg,
		}, nil
	}

	// Mode 2: Direct ECDSA RPC transactions
	if cfg.MockMode || cfg.PrivateKey == "" {
		return &ChainClient{
			CircleClient: circleClient,
			Address:      common.HexToAddress("0x210c024beeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"),
			ChainID:      big.NewInt(cfg.ChainID),
			Config:       cfg,
		}, nil
	}

	client, err := ethclient.Dial(cfg.ArcRPCURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Arc RPC: %w", err)
	}

	cleanKey := strings.TrimPrefix(cfg.PrivateKey, "0x")
	privateKey, err := crypto.HexToECDSA(cleanKey)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("error casting public key to ECDSA")
	}

	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)

	return &ChainClient{
		RPCClient:    client,
		CircleClient: circleClient,
		PrivateKey:   privateKey,
		Address:      fromAddress,
		ChainID:      big.NewInt(cfg.ChainID),
		Config:       cfg,
	}, nil
}

func (c *ChainClient) SignAndSendTx(ctx context.Context, to common.Address, value *big.Int, data []byte) (string, error) {
	if c.Config.MockMode || c.RPCClient == nil {
		fakeTxHash := fmt.Sprintf("0xmock%x", crypto.Keccak256(data)[:16])
		return fakeTxHash, nil
	}

	nonce, err := c.RPCClient.PendingNonceAt(ctx, c.Address)
	if err != nil {
		return "", fmt.Errorf("failed to get nonce: %w", err)
	}

	gasPrice, err := c.RPCClient.SuggestGasPrice(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to suggest gas price: %w", err)
	}

	tx := types.NewTransaction(nonce, to, value, 300000, gasPrice, data)
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(c.ChainID), c.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign transaction: %w", err)
	}

	err = c.RPCClient.SendTransaction(ctx, signedTx)
	if err != nil {
		return "", fmt.Errorf("failed to send transaction: %w", err)
	}

	return signedTx.Hash().Hex(), nil
}
