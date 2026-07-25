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
	Circle struct {
		APIKey       string `yaml:"api_key"`
		EntitySecret string `yaml:"entity_secret"`
		WalletID     string `yaml:"wallet_id"`
	} `yaml:"circle"`
	Arc struct {
		RPCURL               string `yaml:"rpc_url"`
		ChainID              int64  `yaml:"chain_id"`
		PrivateKey           string `yaml:"private_key"`
		ExecutionLogAddress  string `yaml:"execution_log_address"`
		USDCAddress          string `yaml:"usdc_address"`
		EURCAddress          string `yaml:"eurc_address"`
	} `yaml:"arc"`
	WhatsApp struct {
		VerifyToken   string `yaml:"verify_token"`
		APIToken      string `yaml:"api_token"`
		PhoneNumberID string `yaml:"phone_number_id"`
	} `yaml:"whatsapp"`
	Server struct {
		Port string `yaml:"port"`
	} `yaml:"server"`
}

type Config struct {
	ExecutionMode               string
	MockMode                    bool
	Port                        string
	ArcRPCURL                   string
	ChainID                     int64
	PrivateKey                  string
	ExecutionLogContractAddress string
	USDCContractAddress         string
	EURCContractAddress         string
	CircleAPIKey                string
	CircleEntitySecret          string
	CircleWalletID              string
	WhatsAppVerifyToken         string
	WhatsAppAPIToken            string
	WhatsAppPhoneNumberID       string
	TwilioAccountSID            string
	TwilioAuthToken             string
	TwilioWhatsAppNumber        string
}

func LoadConfig() *Config {
	if err := godotenv.Load(); err != nil {
		log.Println("[Config] No .env file found, relying on config.yaml and environment")
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

	mode := getEnv("EXECUTION_MODE", y.Execution.Mode)
	if mode == "" {
		mode = "direct"
	}

	mockStr := getEnv("ROVA_MOCK_MODE", "")
	mockMode := y.Execution.MockMode
	if mockStr == "true" {
		mockMode = true
	} else if mockStr == "false" {
		mockMode = false
	}

	return &Config{
		ExecutionMode:               mode,
		MockMode:                    mockMode,
		Port:                        getEnv("PORT", fallback(y.Server.Port, "8080")),
		ArcRPCURL:                   getEnv("ARC_RPC_URL", fallback(y.Arc.RPCURL, "https://testnet.arc.network/rpc")),
		ChainID:                     fallbackInt(y.Arc.ChainID, 5042002),
		PrivateKey:                  getEnv("ROVA_AGENT_PRIVATE_KEY", y.Arc.PrivateKey),
		ExecutionLogContractAddress: getEnv("NEXT_PUBLIC_ROVA_EXECUTION_LOG_ADDRESS", fallback(y.Arc.ExecutionLogAddress, "0x0000000000000000000000000000000000000000")),
		USDCContractAddress:         getEnv("ARC_USDC_ADDRESS", fallback(y.Arc.USDCAddress, "0x3600000000000000000000000000000000000000")),
		EURCContractAddress:         getEnv("ARC_EURC_ADDRESS", fallback(y.Arc.EURCAddress, "0x3600000000000000000000000000000000000001")),
		CircleAPIKey:                getEnv("CIRCLE_API_KEY", y.Circle.APIKey),
		CircleEntitySecret:          getEnv("CIRCLE_ENTITY_SECRET", y.Circle.EntitySecret),
		CircleWalletID:              getEnv("CIRCLE_WALLET_ID", y.Circle.WalletID),
		WhatsAppVerifyToken:         getEnv("WHATSAPP_VERIFY_TOKEN", fallback(y.WhatsApp.VerifyToken, "rova-secret-verify-token")),
		WhatsAppAPIToken:            getEnv("WHATSAPP_API_TOKEN", y.WhatsApp.APIToken),
		WhatsAppPhoneNumberID:       getEnv("WHATSAPP_PHONE_NUMBER_ID", y.WhatsApp.PhoneNumberID),
		TwilioAccountSID:            getEnv("TWILIO_ACCOUNT_SID", ""),
		TwilioAuthToken:             getEnv("TWILIO_AUTH_TOKEN", ""),
		TwilioWhatsAppNumber:        getEnv("TWILIO_WHATSAPP_NUMBER", "+14155238886"),
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
