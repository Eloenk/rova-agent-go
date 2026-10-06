package config

import (
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

type YAMLConfig struct {
	Execution struct {
		Mode string `yaml:"mode"`
	} `yaml:"execution"`
	AI struct {
		Provider           string `yaml:"provider"`
		Model              string `yaml:"model"`
		AllowRegexFallback bool   `yaml:"allow_regex_fallback"`
	} `yaml:"ai"`
	Balance struct {
		Mode            string `yaml:"mode"`
		PollIntervalSec int    `yaml:"poll_interval_sec"`
	} `yaml:"balance"`
	Arc struct {
		RPCURL              string   `yaml:"rpc_url"`
		RPCURLs             []string `yaml:"rpc_urls"`
		WSSURLs             []string `yaml:"wss_urls"`
		ChainID             int64    `yaml:"chain_id"`
		ExecutionLogAddress string   `yaml:"execution_log_address"`
		USDCAddress         string   `yaml:"usdc_address"`
		EURCAddress         string   `yaml:"eurc_address"`
	} `yaml:"arc"`
	Server struct {
		Port string `yaml:"port"`
	} `yaml:"server"`
	App struct {
		URL string `yaml:"url"`
	} `yaml:"app"`
	Vault struct {
		Strategy string `yaml:"strategy"`
	} `yaml:"vault"`
	Swap struct {
		Strategy      string `yaml:"strategy"`
		SidecarSocket string `yaml:"sidecar_socket"`
		RouterAddress string `yaml:"router_address"`
		SlippageBps   int    `yaml:"slippage_bps"`
	} `yaml:"swap"`
}

type Config struct {
	ExecutionMode               string
	ExecutionEnabled            bool
	AutonomousExecutionEnabled  bool
	WhatsAppExecutionEnabled    bool
	MaxAutonomousAmount         float64
	MaxWhatsAppActionAmount     float64
	AllowRegexFallback          bool
	AIProvider                  string
	AIModel                     string
	Port                        string
	BindAddress                 string
	EngineAPIToken              string
	GoogleGenerativeAIAPIKey    string
	AnthropicAPIKey             string
	ArcRPCURL                   string
	ArcRPCURLs                  []string
	ArcWSSURLs                  []string
	BalanceMode                 string
	BalancePollInterval         int
	ChainID                     int64
	PrivateKey                  string
	ExecutionLogContractAddress string
	USDCContractAddress         string
	EURCContractAddress         string
	CircleAPIKey                string
	CircleEntitySecret          string
	CircleWalletID              string
	WhatsAppAPIToken            string
	WhatsAppPhoneNumberID       string
	SupabaseURL                 string
	SupabaseServiceRoleKey      string
	AppURL                      string
	VaultStrategy               string
	SwapStrategy                string
	SwapSidecarSocket           string
	SwapSidecarToken            string
	SwapRouterAddress           string
}

func LoadConfig() *Config {
	envFiles := []string{".env.local", ".env", "../.env.local", "../.env", "../../rova/.env.local"}
	for _, file := range envFiles {
		_ = godotenv.Overload(file)
	}

	var y YAMLConfig
	yamlBytes, err := os.ReadFile("config.yaml")
	if err == nil {
		if err := yaml.Unmarshal(yamlBytes, &y); err != nil {
			log.Printf("[Config] Warning unmarshaling config.yaml: %v", err)
		}
	} else {
		log.Println("[Config] No config.yaml found, using defaults")
	}

	mode := y.Execution.Mode
	if mode == "" {
		mode = getEnv("EXECUTION_MODE", "direct")
	}

	rpcURLs := y.Arc.RPCURLs
	if len(rpcURLs) == 0 {
		defaultRPC := getEnv("ARC_RPC_URL", fallback(y.Arc.RPCURL, "https://arc-testnet.drpc.org"))
		rpcURLs = []string{defaultRPC, "https://rpc.testnet.arc.network"}
	}

	wssURLs := y.Arc.WSSURLs
	if len(wssURLs) == 0 {
		defaultWSS := getEnv("ARC_WSS_URL", "wss://arc-testnet.drpc.org/ws")
		wssURLs = []string{defaultWSS, "wss://wss.testnet.arc.network"}
	}

	balanceMode := getEnv("ROVA_BALANCE_MODE", fallback(y.Balance.Mode, "wss"))
	pollInterval := y.Balance.PollIntervalSec
	if pollInterval <= 0 {
		pollInterval = 5
	}

	return &Config{
		ExecutionMode:               mode,
		ExecutionEnabled:            getBoolEnv("ROVA_EXECUTION_ENABLED", false),
		AutonomousExecutionEnabled:  getBoolEnv("ROVA_AUTONOMOUS_EXECUTION_ENABLED", false),
		WhatsAppExecutionEnabled:    getBoolEnv("ROVA_WHATSAPP_EXECUTION_ENABLED", false),
		MaxAutonomousAmount:         getPositiveFloatEnv("ROVA_MAX_AUTONOMOUS_AMOUNT_USDC", 100),
		MaxWhatsAppActionAmount:     getPositiveFloatEnv("ROVA_MAX_WHATSAPP_AMOUNT_USDC", 100),
		AllowRegexFallback:          y.AI.AllowRegexFallback,
		AIProvider:                  fallback(y.AI.Provider, "auto"),
		AIModel:                     y.AI.Model,
		Port:                        getEnv("PORT", fallback(y.Server.Port, "8080")),
		BindAddress:                 getEnv("ROVA_ENGINE_BIND", "127.0.0.1"),
		EngineAPIToken:              os.Getenv("ROVA_ENGINE_API_TOKEN"),
		GoogleGenerativeAIAPIKey:    os.Getenv("GOOGLE_GENERATIVE_AI_API_KEY"),
		AnthropicAPIKey:             os.Getenv("ANTHROPIC_API_KEY"),
		ArcRPCURL:                   rpcURLs[0],
		ArcRPCURLs:                  rpcURLs,
		ArcWSSURLs:                  wssURLs,
		BalanceMode:                 balanceMode,
		BalancePollInterval:         pollInterval,
		ChainID:                     fallbackInt(y.Arc.ChainID, 5042002),
		PrivateKey:                  os.Getenv("ROVA_AGENT_PRIVATE_KEY"),
		ExecutionLogContractAddress: getEnv("NEXT_PUBLIC_ROVA_EXECUTION_LOG_ADDRESS", y.Arc.ExecutionLogAddress),
		USDCContractAddress:         fallback(y.Arc.USDCAddress, os.Getenv("ARC_USDC_ADDRESS")),
		EURCContractAddress:         fallback(y.Arc.EURCAddress, os.Getenv("ARC_EURC_ADDRESS")),
		CircleAPIKey:                os.Getenv("CIRCLE_API_KEY"),
		CircleEntitySecret:          os.Getenv("CIRCLE_ENTITY_SECRET"),
		CircleWalletID:              os.Getenv("CIRCLE_WALLET_ID"),
		WhatsAppAPIToken:            os.Getenv("WHATSAPP_API_TOKEN"),
		WhatsAppPhoneNumberID:       os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
		SupabaseURL:                 getEnv("NEXT_PUBLIC_SUPABASE_URL", os.Getenv("SUPABASE_URL")),
		SupabaseServiceRoleKey:      os.Getenv("SUPABASE_SERVICE_ROLE_KEY"),
		AppURL:                      getEnv("ROVA_APP_URL", getEnv("NEXT_PUBLIC_APP_URL", fallback(y.App.URL, "https://rova-web.vercel.app"))),
		VaultStrategy:               getEnv("ROVA_VAULT_STRATEGY", fallback(y.Vault.Strategy, "smart_contract")),
		SwapStrategy:                getEnv("ROVA_SWAP_STRATEGY", fallback(y.Swap.Strategy, "sidecar_uds")),
		SwapSidecarSocket:           getEnv("ROVA_SWAP_SOCKET", fallback(y.Swap.SidecarSocket, "/tmp/rova-swap.sock")),
		SwapSidecarToken:            os.Getenv("ROVA_SIDECAR_TOKEN"),
		SwapRouterAddress:           getEnv("NEXT_PUBLIC_ROVA_SWAP_ROUTER_ADDRESS", fallback(y.Swap.RouterAddress, "0x8FE6B999Dc680CcFDD5Bf7EB0974218be2542DAA")),
	}
}

func (c *Config) ValidateEngineServer() error {
	if len(c.EngineAPIToken) < 32 {
		return fmt.Errorf("ROVA_ENGINE_API_TOKEN must be configured with at least 32 characters")
	}
	return c.ValidateDatabaseAccess()
}

func (c *Config) ValidateDatabaseAccess() error {
	if c.SupabaseURL == "" || c.SupabaseServiceRoleKey == "" {
		return fmt.Errorf("SUPABASE_URL and SUPABASE_SERVICE_ROLE_KEY are required for the engine")
	}
	return nil
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return fallback
}

func fallback(val, def string) string {
	if val != "" {
		return val
	}
	return def
}

func fallbackInt(val, def int64) int64 {
	if val != 0 {
		return val
	}
	return def
}

func getBoolEnv(key string, fallback bool) bool {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getPositiveFloatEnv(key string, fallback float64) float64 {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
