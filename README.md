# ⚡ Rova Agent Go Engine (`rova-agent-go`)

High-performance, 24/7 autonomous backend daemon & native WhatsApp bot engine for **Rova Agentic Economy**, built in **Go (Golang)** with `go-ethereum` and `whatsmeow`.

---

## 🌟 Architectural Overview

`rova-agent-go` is the decoupled, 24/7 backend engine for Rova. It handles autonomous rate watching, microsecond nanopayment rate shopping, server-side Circle Programmable Wallet execution, and native WhatsApp chat interaction.

```
                  ┌─────────────────────────────────────────┐
                  │          WhatsApp User Chat             │
                  └────────────────────┬────────────────────┘
                                       │ (Whatsmeow Protocol)
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                            rova-agent-go Engine                             │
│                                                                             │
│  ┌───────────────────────┐  ┌─────────────────────┐  ┌───────────────────┐  │
│  │   cmd/whatsapp-bot    │  │   cmd/gen-secret    │  │    cmd/server     │  │
│  │  (Native Whatsmeow)   │  │ (Circle Entity CLI) │  │  (HTTP API Server)│  │
│  └───────────┬───────────┘  └──────────┬──────────┘  └─────────┬─────────┘  │
│              │                         │                       │            │
│              ▼                         ▼                       ▼            │
│   ┌────────────────────┐   ┌───────────────────────┐  ┌──────────────────┐  │
│   │    pkg/whatsapp    │   │      pkg/circle       │  │    pkg/agent     │  │
│   │ (SQLite & Handler) │   │ (HSM & Entity Secret) │  │  (24/7 Watcher)  │  │
│   └────────────────────┘   └───────────┬───────────┘  └────────┬─────────┘  │
│                                        │                       │            │
│                                        ▼                       ▼            │
│                            ┌───────────────────────┐                        │
│                            │       pkg/chain       │                        │
│                            │ (go-ethereum & Arc)   │                        │
│                            └───────────┬───────────┘                        │
└────────────────────────────────────────┼────────────────────────────────────┘
                                         ▼
                   ┌──────────────────────────────────────────┐
                   │    Arc Testnet (Chain ID 5042002)        │
                   │    - USDC / EURC Contracts               │
                   │    - RovaExecutionLog.sol                │
                   └──────────────────────────────────────────┘
```

---

## 🚀 Key Modules & Capabilities

### 1. Standalone Native WhatsApp Bot (`cmd/whatsapp-bot/` & `pkg/whatsapp/meow_bot.go`)
* **Pure Go Multi-Device Engine**: Built using `go.mau.fi/whatsmeow` with zero Node.js/npm or external runtime dependencies.
* **Pure Go SQLite Session Store**: Uses `modernc.org/sqlite` to persist sessions (`rova_whatsapp.db`) without requiring CGO or GCC cross-compilers.
* **Terminal QR-Code Pairing**: Displays a QR code directly in the terminal via `qrterminal` for instant pairing with any WhatsApp account.
* **Command Dispatcher**:
  * `balance` / `wallet`: Displays Circle Programmable Wallet address & custody details.
  * `status` / `rules`: Displays active rate watchers on Arc.
  * `send <amount> USDC to <address>`: Executes USDC transfers on Arc and returns ArcScan link.
  * `swap <amount> USDC to EURC`: Triggers StableFX atomic swaps.
  * `bridge <amount> USDC from <chain> to Arc`: Triggers CCTP V2 cross-chain bridging.

### 2. Circle Entity Secret Generator CLI (`cmd/gen-secret/` & `pkg/circle/secret.go`)
* **32-Byte Secret Key Generation**: Uses `crypto/rand` to generate cryptographically secure 64-character hex strings for `CIRCLE_ENTITY_SECRET`.
* **RSA-OAEP SHA-256 Ciphertext Encryption**: Takes Circle Console's Developer Public Key and outputs the exact base64 ciphertext needed to register entity secrets in Circle Console.

### 3. Circle Developer-Controlled Wallets SDK Client (`pkg/circle/client.go`)
* Direct HTTP client handling Circle's Developer-Controlled Wallets API for transfers, contract executions, and HSM transaction signing.

### 4. Microsecond Goroutine Nanopayments (`pkg/nanopay/x402.go`)
* Executes parallel RFQ quote queries across multiple rate providers simultaneously using Go goroutines (`sync.WaitGroup`) to pick the best rate before every trade.

### 5. Arc Smart Contract Integration (`pkg/chain/`)
* Native `go-ethereum` (`ethclient`) binding supporting EIP-1559 transaction signing on Arc Testnet (Chain ID `5042002`).
* Direct ABI contract calls to `RovaExecutionLog.sol` for on-chain audit trail recording.

---

## 🛠️ Folder Structure

```
rova-agent-go/
├── cmd/
│   ├── gen-secret/        # Circle Entity Secret & Ciphertext Generator CLI
│   │   └── main.go
│   ├── server/            # Rova Backend HTTP Server Entrypoint
│   │   └── main.go
│   └── whatsapp-bot/      # Standalone Whatsmeow Bot Executable Entrypoint
│       └── main.go
├── pkg/
│   ├── agent/             # 24/7 Ticker Watcher & State Store
│   ├── chain/             # go-ethereum Arc Testnet Client & ERC-20 Bindings
│   ├── circle/            # Circle Developer-Controlled Wallets & Entity Secret Crypto
│   ├── config/            # YAML & Environment Configuration Loader
│   ├── nanopay/           # x402 Goroutine Nanopayment Rate Shopping Engine
│   ├── rpc/               # JSON-RPC Handlers
│   └── whatsapp/          # Whatsmeow SQLite Bot & Webhook Notifier
├── bin/                   # Compiled Binaries (whatsapp-bot.exe, server.exe, gen-secret.exe)
├── config.yaml            # Engine Configuration File
├── go.mod                 # Go Modules & Dependencies
├── go.sum                 # Module Checksums
└── README.md              # Documentation
```

---

## ⚙️ How to Run

### Prerequisites
* **Go 1.22+** installed on your system.

---

### A. Run the Standalone WhatsApp Bot (`whatsmeow`)

```bash
# 1. Run directly with Go
go run ./cmd/whatsapp-bot

# 2. Or build and run the compiled binary
go build -o bin/whatsapp-bot.exe ./cmd/whatsapp-bot
./bin/whatsapp-bot.exe
```

* **First Run**: Scan the Terminal QR Code with WhatsApp (**Linked Devices** $\rightarrow$ **Link a Device**).
* **Subsequent Runs**: Automatically reconnects using the persisted SQLite session store (`rova_whatsapp.db`).

---

### B. Generate a Circle Entity Secret & Registration Ciphertext

```bash
# Generate 64-char Hex Secret:
go run ./cmd/gen-secret

# Generate Secret AND Ciphertext for Circle Console:
go run ./cmd/gen-secret -pubkey "<paste-circle-developer-pubkey-here>"
```

---

### C. Run the Backend HTTP API Server

```bash
go run ./cmd/server
```

---

## 🔑 Environment Variables (`.env.local` / `.env`)

```env
# Server & Environment
PORT=8080
MOCK_MODE=false

# Circle Developer-Controlled Wallets
CIRCLE_API_KEY=TEST_API_KEY:...
CIRCLE_ENTITY_SECRET=3a9f8b1c4e2d... (64 hex characters)
CIRCLE_WALLET_ID=your_circle_wallet_id

# Arc Blockchain Configuration
ARC_RPC_URL=https://rpc.testnet.arc.network
ROVA_EXECUTION_LOG_ADDRESS=0x58d1e3e11C7a93cb26C371B115f2710aF68d427a
```
