package circle

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
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

// EncryptEntitySecretCiphertext encrypts a 32-byte raw entity secret using a Circle RSA Public Key (PEM format or Base64)
// into a Base64-encoded ciphertext for registering in Circle Console.
func EncryptEntitySecretCiphertext(secretBytes []byte, pubKeyPEM string) (string, error) {
	block, _ := pem.Decode([]byte(pubKeyPEM))
	var derBytes []byte
	if block != nil {
		derBytes = block.Bytes
	} else {
		b64Bytes, err := base64.StdEncoding.DecodeString(pubKeyPEM)
		if err != nil {
			return "", errors.New("invalid public key: must be PEM format or Base64 encoded DER")
		}
		derBytes = b64Bytes
	}

	pub, err := x509.ParsePKIXPublicKey(derBytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse public key: %w", err)
	}

	rsaPubKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		return "", errors.New("public key is not an RSA key")
	}

	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaPubKey, secretBytes, nil)
	if err != nil {
		return "", fmt.Errorf("RSA-OAEP encryption failed: %w", err)
	}

	return base64.StdEncoding.EncodeToString(ciphertext), nil
}
