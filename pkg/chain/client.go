package chain

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
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
		}
		return &ChainClient{
			CircleClient: circleClient,
			Address:      addr,
			ChainID:      big.NewInt(cfg.ChainID),
			Config:       cfg,
		}, nil
	}

	// Mode 2: Direct ECDSA RPC transactions (or Read-Only RPC)
	var client *ethclient.Client
	if cfg.ArcRPCURL != "" {
		c, err := ethclient.Dial(cfg.ArcRPCURL)
		if err == nil {
			client = c
		}
	}

	var privateKey *ecdsa.PrivateKey
	var fromAddress common.Address

	if cfg.PrivateKey != "" {
		cleanKey := strings.TrimPrefix(cfg.PrivateKey, "0x")
		pk, err := crypto.HexToECDSA(cleanKey)
		if err == nil {
			privateKey = pk
			if pubKeyECDSA, ok := pk.Public().(*ecdsa.PublicKey); ok {
				fromAddress = crypto.PubkeyToAddress(*pubKeyECDSA)
			}
		} else {
			log.Printf("[ChainClient] Warning: Failed to parse private key: %v", err)
		}
	} else {
		log.Println("[ChainClient] Notice: ROVA_AGENT_PRIVATE_KEY not set. Operating in read-only / Circle custody mode.")
	}

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
	if c.Config == nil || !c.Config.ExecutionEnabled {
		return "", fmt.Errorf("transaction execution is disabled; set ROVA_EXECUTION_ENABLED=true only after operational approval")
	}
	if c.RPCClient == nil || c.PrivateKey == nil {
		return "", fmt.Errorf("RPC client and private key are required to sign and send transactions")
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

func (c *ChainClient) GetBalanceERC20WithFailover(ctx context.Context, walletAddress string, tokenAddress string) (float64, error) {
	if walletAddress == "" || !strings.HasPrefix(walletAddress, "0x") || len(walletAddress) < 42 {
		return 0, fmt.Errorf("invalid wallet address: %s", walletAddress)
	}

	urls := c.Config.ArcRPCURLs
	if len(urls) == 0 {
		urls = []string{c.Config.ArcRPCURL}
	}

	tokenAddr := common.HexToAddress(tokenAddress)
	targetAddress := common.HexToAddress(walletAddress)
	data := append(common.Hex2Bytes("70a08231"), common.LeftPadBytes(targetAddress.Bytes(), 32)...)

	var lastErr error
	for _, rpcUrl := range urls {
		dialClient, err := ethclient.Dial(rpcUrl)
		if err != nil {
			lastErr = err
			continue
		}
		res, err := dialClient.CallContract(ctx, ethereum.CallMsg{
			To:   &tokenAddr,
			Data: data,
		}, nil)
		dialClient.Close()

		if err != nil {
			lastErr = err
			continue
		}
		if len(res) == 0 {
			return 0, nil
		}

		bal := new(big.Int).SetBytes(res)
		balFloat := new(big.Float).SetInt(bal)
		decimals := new(big.Float).SetFloat64(1000000.0)
		resFloat, _ := new(big.Float).Quo(balFloat, decimals).Float64()
		return resFloat, nil
	}

	return 0, fmt.Errorf("all RPC endpoints failed, last error: %v", lastErr)
}

func (c *ChainClient) GetBalanceUSDCWithFailover(ctx context.Context, walletAddress string) (float64, error) {
	return c.GetBalanceERC20WithFailover(ctx, walletAddress, c.Config.USDCContractAddress)
}

func (c *ChainClient) GetBalanceEURCWithFailover(ctx context.Context, walletAddress string) (float64, error) {
	return c.GetBalanceERC20WithFailover(ctx, walletAddress, c.Config.EURCContractAddress)
}

func (c *ChainClient) ListenUSDCTransferEvents(ctx context.Context, getTargetWallets func() []string, onTransfer func(toAddress string, amount float64, txHash string)) {
	wssURLs := c.Config.ArcWSSURLs
	if len(wssURLs) == 0 {
		wssURLs = []string{"wss://arc-testnet.drpc.org", "wss://rpc.testnet.arc.network"}
	}

	usdcAddressHex := c.Config.USDCContractAddress
	usdcAddress := common.HexToAddress(usdcAddressHex)
	transferTopic := crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			targetWallets := getTargetWallets()
			if len(targetWallets) == 0 {
				time.Sleep(5 * time.Second)
				continue
			}

			toTopicHashes := make([]common.Hash, 0, len(targetWallets))
			targetMap := make(map[string]bool)
			for _, w := range targetWallets {
				cleanW := strings.TrimSpace(w)
				if strings.HasPrefix(cleanW, "0x") && len(cleanW) == 42 {
					targetMap[strings.ToLower(cleanW)] = true
					toTopicHashes = append(toTopicHashes, common.BytesToHash(common.LeftPadBytes(common.HexToAddress(cleanW).Bytes(), 32)))
				}
			}

			if len(toTopicHashes) == 0 {
				time.Sleep(5 * time.Second)
				continue
			}

			for _, wssURL := range wssURLs {
				log.Printf("[Chain WSS] Connecting to WebSocket log stream for %d active rule wallet(s): %s", len(toTopicHashes), wssURL)
				client, err := ethclient.DialContext(ctx, wssURL)
				if err != nil {
					log.Printf("[Chain WSS] Connection failed for %s: %v", wssURL, err)
					time.Sleep(2 * time.Second)
					continue
				}

				query := ethereum.FilterQuery{
					Addresses: []common.Address{usdcAddress},
					Topics:    [][]common.Hash{{transferTopic}, nil, toTopicHashes},
				}

				logs := make(chan types.Log)
				sub, err := client.SubscribeFilterLogs(ctx, query, logs)
				if err != nil {
					log.Printf("[Chain WSS] Subscription failed for %s: %v", wssURL, err)
					client.Close()
					time.Sleep(2 * time.Second)
					continue
				}

				log.Printf("[Chain WSS] Subscribed to real-time USDC Transfer events ONLY for %d target wallet(s) on %s", len(toTopicHashes), wssURL)

				for {
					select {
					case <-ctx.Done():
						sub.Unsubscribe()
						client.Close()
						return
					case err := <-sub.Err():
						log.Printf("[Chain WSS] Subscription error: %v", err)
						sub.Unsubscribe()
						client.Close()
						goto RECONNECT
					case vLog := <-logs:
						if len(vLog.Topics) >= 3 {
							toAddr := common.BytesToAddress(vLog.Topics[2].Bytes()).Hex()
							if targetMap[strings.ToLower(toAddr)] {
								rawAmount := new(big.Int).SetBytes(vLog.Data)
								balFloat := new(big.Float).SetInt(rawAmount)
								decimals := new(big.Float).SetFloat64(1000000.0)
								amount, _ := new(big.Float).Quo(balFloat, decimals).Float64()

								txHash := vLog.TxHash.Hex()
								log.Printf("[Chain WSS Event] Real-Time USDC Transfer Detected for Rule Wallet! To: %s, Amount: %.2f USDC, Tx: %s", toAddr, amount, txHash)
								onTransfer(toAddr, amount, txHash)
							}
						}
					}
				}

			RECONNECT:
				time.Sleep(2 * time.Second)
			}
		}
	}()
}
