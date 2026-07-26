package main

import (
	"fmt"
	"os"

	"rova-agent-go/pkg/circle"
)

func main() {
	hexSecret, _, err := circle.GenerateEntitySecret()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating entity secret: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("==================================================================")
	fmt.Println("             ROVA AGENT GO — CIRCLE ENTITY SECRET                 ")
	fmt.Println("==================================================================")
	fmt.Println()
	fmt.Println("🔐 Your 32-Byte Entity Secret (Copy into .env / .env.local):")
	fmt.Println("------------------------------------------------------------------")
	fmt.Println("CIRCLE_ENTITY_SECRET=" + hexSecret)
	fmt.Println("------------------------------------------------------------------")
	fmt.Println()
	fmt.Println("==================================================================")
}
