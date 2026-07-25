package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"rova-agent-go/pkg/circle"
)

func main() {
	pubKeyFlag := flag.String("pubkey", "", "Circle Public Key (PEM string or file path) to generate ciphertext")
	flag.Parse()

	hexSecret, secretBytes, err := circle.GenerateEntitySecret()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating entity secret: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("==================================================================")
	fmt.Println("             ROVA AGENT GO — CIRCLE ENTITY SECRET                 ")
	fmt.Println("==================================================================")
	fmt.Println()
	fmt.Println("🔐 Your 32-Byte Entity Secret (Save in .env as CIRCLE_ENTITY_SECRET):")
	fmt.Println("------------------------------------------------------------------")
	fmt.Println(hexSecret)
	fmt.Println("------------------------------------------------------------------")
	fmt.Println()

	pubKey := strings.TrimSpace(*pubKeyFlag)
	if pubKey != "" {
		if fileContent, err := os.ReadFile(pubKey); err == nil {
			pubKey = string(fileContent)
		}

		ciphertext, err := circle.EncryptEntitySecretCiphertext(secretBytes, pubKey)
		if err != nil {
			fmt.Printf("⚠️ Could not encrypt with provided public key: %v\n", err)
		} else {
			fmt.Println("📦 Ciphertext for Circle Console Registration:")
			fmt.Println("------------------------------------------------------------------")
			fmt.Println(ciphertext)
			fmt.Println("------------------------------------------------------------------")
			fmt.Println()
		}
	} else {
		fmt.Println("💡 Tip: To generate the ciphertext ready for Circle Console registration,")
		fmt.Println("   run with your Circle Developer Public Key:")
		fmt.Println("   go run ./cmd/gen-secret -pubkey \"<paste-circle-pubkey-here>\"")
	}

	fmt.Println("==================================================================")
}
