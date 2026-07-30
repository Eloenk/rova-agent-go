package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

type YAMLConfig struct {
	Execution struct {
		Mode     string `yaml:"mode"`
		MockMode bool   `yaml:"mock_mode"`
	} `yaml:"execution"`
	AI struct {
		Provider           string `yaml:"provider"`
		Model              string `yaml:"model"`
		AllowRegexFallback bool   `yaml:"allow_regex_fallback"`
	} `yaml:"ai"`
	Arc struct {
		RPCURL              string `yaml:"rpc_url"`
		ChainID             int64  `yaml:"chain_id"`
		ExecutionLogAddress string `yaml:"execution_log_address"`
		USDCAddress         string `yaml:"usdc_address"`
		EURCAddress         string `yaml:"eurc_address"`
	} `yaml:"arc"`
	Server struct {
		Port string `yaml:"port"`
	} `yaml:"server"`
}

type Config struct {
	ExecutionMode               string
	MockMode                    bool
	AllowRegexFallback          bool
	AIProvider                  string
	AIModel                     string
	Port                        string
	GoogleGenerativeAIAPIKey    string
	AnthropicAPIKey             string
	ArcRPCURL                   string
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
	SupabaseAnonKey             string
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

	mockMode := y.Execution.MockMode
	if mockStr := os.Getenv("ROVA_MOCK_MODE"); mockStr == "true" {
		mockMode = true
	} else if mockStr == "false" {
		mockMode = false
	}

	return &Config{
		ExecutionMode:               mode,
		MockMode:                    mockMode,
		AllowRegexFallback:          y.AI.AllowRegexFallback,
		AIProvider:                  fallback(y.AI.Provider, "auto"),
		AIModel:                     y.AI.Model,
		Port:                        getEnv("PORT", fallback(y.Server.Port, "8080")),
		GoogleGenerativeAIAPIKey:    os.Getenv("GOOGLE_GENERATIVE_AI_API_KEY"),
		AnthropicAPIKey:             os.Getenv("ANTHROPIC_API_KEY"),
		ArcRPCURL:                   getEnv("ARC_RPC_URL", fallback(y.Arc.RPCURL, "https://testnet.arc.network/rpc")),
		ChainID:                     fallbackInt(y.Arc.ChainID, 5042002),
		PrivateKey:                  os.Getenv("ROVA_AGENT_PRIVATE_KEY"),
		ExecutionLogContractAddress: getEnv("NEXT_PUBLIC_ROVA_EXECUTION_LOG_ADDRESS", fallback(y.Arc.ExecutionLogAddress, "0x0000000000000000000000000000000000000000")),
		USDCContractAddress:         getEnv("ARC_USDC_ADDRESS", fallback(y.Arc.USDCAddress, "0x3600000000000000000000000000000000000000")),
		EURCContractAddress:         getEnv("ARC_EURC_ADDRESS", fallback(y.Arc.EURCAddress, "0x3600000000000000000000000000000000000001")),
		CircleAPIKey:                os.Getenv("CIRCLE_API_KEY"),
		CircleEntitySecret:          os.Getenv("CIRCLE_ENTITY_SECRET"),
		CircleWalletID:              os.Getenv("CIRCLE_WALLET_ID"),
		WhatsAppAPIToken:            os.Getenv("WHATSAPP_API_TOKEN"),
		WhatsAppPhoneNumberID:       os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
		SupabaseURL:                 getEnv("NEXT_PUBLIC_SUPABASE_URL", os.Getenv("SUPABASE_URL")),
		SupabaseAnonKey:             getEnv("NEXT_PUBLIC_SUPABASE_ANON_KEY", os.Getenv("SUPABASE_ANON_KEY")),
	}
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
