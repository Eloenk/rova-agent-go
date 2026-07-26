package circle

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GenerateEntitySecret generates a cryptographically secure 32-byte (64 hex characters) entity secret.
func GenerateEntitySecret() (string, []byte, error) {
	bytes := make([]byte, 32)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", nil, fmt.Errorf("failed to generate random bytes: %w", err)
	}
	hexSecret := hex.EncodeToString(bytes)
	return hexSecret, bytes, nil
}

// FetchCirclePublicKey fetches the Circle Developer Public Key directly from Circle API using CIRCLE_API_KEY.
func FetchCirclePublicKey(apiKey string) (string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", errors.New("CIRCLE_API_KEY is required to fetch public key from Circle")
	}

	url := "https://api.circle.com/v1/w3s/config/entity/publicKey"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch public key from Circle API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("circle API HTTP %d: %s", resp.StatusCode, string(body))
	}

	var res struct {
		Data struct {
			PublicKey string `json:"publicKey"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", fmt.Errorf("failed to parse Circle API response: %w", err)
	}

	if res.Data.PublicKey == "" {
		return "", errors.New("circle API returned empty public key")
	}

	return res.Data.PublicKey, nil
}

// EncryptEntitySecretCiphertext encrypts a 32-byte raw entity secret using a Circle RSA Public Key
// (PEM format, PKCS#1, Base64 DER, or Hex DER) into a Base64-encoded ciphertext for registering in Circle Console.
func EncryptEntitySecretCiphertext(secretBytes []byte, pubKeyStr string) (string, error) {
	pubKeyStr = strings.TrimSpace(pubKeyStr)
	if len(pubKeyStr) == 0 {
		return "", errors.New("public key string cannot be empty")
	}

	var derBytes []byte
	block, _ := pem.Decode([]byte(pubKeyStr))

	if block != nil {
		derBytes = block.Bytes
	} else if hexBytes, err := hex.DecodeString(pubKeyStr); err == nil {
		derBytes = hexBytes
	} else if b64Bytes, err := base64.StdEncoding.DecodeString(pubKeyStr); err == nil {
		derBytes = b64Bytes
	} else {
		return "", errors.New("invalid public key format: must be PEM, Hex, or Base64 encoded DER")
	}

	var rsaPubKey *rsa.PublicKey
	if pub, err := x509.ParsePKIXPublicKey(derBytes); err == nil {
		if k, ok := pub.(*rsa.PublicKey); ok {
			rsaPubKey = k
		}
	}

	if rsaPubKey == nil {
		if k, err := x509.ParsePKCS1PublicKey(derBytes); err == nil {
			rsaPubKey = k
		}
	}

	if rsaPubKey == nil {
		return "", errors.New("failed to parse RSA public key (tried PKIX and PKCS#1 formats)")
	}

	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaPubKey, secretBytes, nil)
	if err != nil {
		return "", fmt.Errorf("RSA-OAEP encryption failed: %w", err)
	}

	return base64.StdEncoding.EncodeToString(ciphertext), nil
}
