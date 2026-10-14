# ⚡ Rova Agent Go Engine (`rova-agent-go`)

High-performance backend daemon and native WhatsApp bot engine for **Rova Agentic Economy**, built in **Go (Golang)** with `go-ethereum` and `whatsmeow`.

> **Release posture:** Rova is testnet-only. Transaction execution, autonomous execution, and WhatsApp execution are disabled by default and require separate server-side gates. This repository does not authorize a mainnet deployment.

---

## 🌟 Architectural Overview

`rova-agent-go` is the decoupled backend engine for Rova. It handles policy-gated rate watching, parallel quote simulation, server-side Circle Programmable Wallet execution, deterministic AI failsafe intent parsing, and native WhatsApp chat interaction. Autonomous work is claimed atomically in Supabase and remains fail-closed if the database is unavailable.

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

### 1. Conversational Fast-Path (`pkg/ai/parser.go`)
* **Zero-Latency Greeting Response**: Deterministically catches greetings ("hi", "who are you", "what can you do") before calling external AI providers.
* **Deterministic Guardrails**: Marks non-financial interactions as non-executable to prevent unauthorized transaction execution.

### 2. Standalone Native WhatsApp Bot (`cmd/whatsapp-bot/` & `pkg/whatsapp/meow_bot.go`)
* **Pure Go Multi-Device Engine**: Built using `go.mau.fi/whatsmeow` with zero Node.js/npm or external runtime dependencies.
* **Pure Go SQLite Session Store**: Uses `modernc.org/sqlite` to persist sessions (`rova_whatsapp.db`) without requiring CGO or GCC cross-compilers.
* **Terminal QR-Code Pairing**: Displays a QR code directly in the terminal via `qrterminal` for instant pairing with any WhatsApp account.
* **Command Dispatcher**:
  * `balance` / `wallet`: Displays Circle Programmable Wallet address & custody details.
  * `status` / `rules`: Displays active rate watchers on Arc.
  * `send <amount> USDC to <address>`: Executes USDC transfers on Arc and returns ArcScan link.
  * `swap <amount> USDC to EURC`: Triggers StableFX atomic swaps.
  * `bridge <amount> USDC from <chain> to Arc`: Triggers CCTP V2 cross-chain bridging.

### 3. Circle Entity Secret Generator CLI (`cmd/gen-secret/` & `pkg/circle/secret.go`)
* **32-Byte Secret Key Generation**: Uses `crypto/rand` to generate cryptographically secure 64-character hex strings for `CIRCLE_ENTITY_SECRET`.
* **RSA-OAEP SHA-256 Ciphertext Encryption**: Takes Circle Console's Developer Public Key and outputs the exact base64 ciphertext needed to register entity secrets in Circle Console.

### 4. Circle Developer-Controlled Wallets SDK Client (`pkg/circle/client.go`)
* Direct HTTP client handling Circle's Developer-Controlled Wallets API for transfers, contract executions, and HSM transaction signing.

### 5. Parallel Quote Simulation (`pkg/nanopay/shop.go`)
* Compares three simulated provider quotes concurrently using Go goroutines (`sync.WaitGroup`).
* Simulated quotes are marked non-executable and cannot trigger transfers. Live x402 verification and settlement remain an explicit future integration gate.

### 6. Arc Smart Contract Integration (`pkg/chain/`)
* Native `go-ethereum` (`ethclient`) binding for Arc Testnet reads and explicitly gated direct transaction signing (Chain ID `5042002`).
* Direct ABI contract calls to `RovaExecutionLog.sol` for on-chain audit trail recording.

### 7. Durable Autonomous Execution (`pkg/agent/`)
* Supabase RPC claims atomically move an active rule or standing intent to `ready_to_execute`, preventing two engine instances from executing the same record.
* Claims succeed only for managed-custody records whose `owner_email`, `source_wallet`, and `users.circle_wallet_address` mapping agree.
* The engine executes the exact row returned by the claim, writes the transaction receipt before re-arming or finalizing the policy, and leaves uncertain failures paused for operator reconciliation.
* Realtime transfer events are evaluation signals only; a fresh balance read and database claim are still required before execution.

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
│   ├── ai/                # Intent Parsing & Failsafe Greeting Fast-Path
│   ├── chain/             # go-ethereum Arc Testnet Client & ERC-20 Bindings
│   ├── circle/            # Circle Developer-Controlled Wallets & Entity Secret Crypto
│   ├── config/            # YAML & Environment Configuration Loader
│   ├── nanopay/           # Non-executable parallel quote simulation
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
* **Go 1.25+** installed on your system.
* **Node.js 20+** only when running the optional Circle SwapKit sidecar.

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

Apply the web repository's complete `schema.sql` to the same Supabase project first. The engine requires the service-role-only `claim_agent_rule` and `claim_standing_intent` functions and will not fall back to stale cached rules when persistence is unavailable.

```bash
go run ./cmd/server
```

### D. Run the Optional Swap Sidecar

The sidecar reads runtime values only through `sidecar/config.js`, binds to a Unix socket by default (or loopback TCP on Windows), and requires bearer authentication. Install with scripts disabled, keep the execution gate off until an approved testnet rehearsal, then start it separately:

```bash
cd sidecar
npm ci --ignore-scripts
npm start
```

---

## 🔑 Runtime Configuration

`pkg/config/config.go` is the engine's typed configuration boundary. It combines non-secret defaults from `config.yaml` with credentials and deployment-specific values supplied by the process environment. Production secrets belong in the hosting platform's secret manager, not in TypeScript/Go source, YAML, committed `.env` files, logs, or chat.

Copy `.env.example` only for local development. Every `.env` variant except `.env.example` is ignored by Git. The engine does not read dotenv files unless the operator explicitly sets `ROVA_LOAD_DOTENV=true` in the parent process; dotenv values never override already injected environment variables. Do not share one `.env` file across users or environments.

```env
# Bind locally unless a protected reverse proxy is configured
EXECUTION_MODE=circle
ROVA_ENGINE_BIND=127.0.0.1
PORT=8080
ROVA_ENGINE_API_TOKEN=

# Independent execution gates; keep false outside an approved testnet rehearsal
ROVA_EXECUTION_ENABLED=false
ROVA_AUTONOMOUS_EXECUTION_ENABLED=false
ROVA_WHATSAPP_EXECUTION_ENABLED=false
ROVA_MAX_AUTONOMOUS_AMOUNT_USDC=100
ROVA_MAX_WHATSAPP_AMOUNT_USDC=100

# Circle Developer-Controlled Wallets
CIRCLE_API_KEY=
CIRCLE_ENTITY_SECRET=
CIRCLE_WALLET_ID=

# Server-only persistence
SUPABASE_URL=
SUPABASE_SECRET_KEY=
# Optional Realtime key; when blank, the engine polls through server-only REST.
SUPABASE_PUBLISHABLE_KEY=

# Authenticated Circle SwapKit sidecar
ROVA_SIDECAR_TOKEN=
ROVA_SWAP_SOCKET=/tmp/rova-swap.sock
SWAP_SIDECAR_PORT=
ARC_CHAIN=Arc_Testnet
ROVA_SAVINGS_VAULT_ADDRESS=0x9330DA5152Cc676a029cfaCCcA3948e11EDE9BfB

# Arc testnet configuration
ARC_RPC_URL=https://rpc.testnet.arc.network
NEXT_PUBLIC_ROVA_EXECUTION_LOG_ADDRESS=0x58d1e3e11C7a93cb26C371B115f2710aF68d427a

# Direct signing is optional and discouraged; prefer Circle custody
ROVA_AGENT_PRIVATE_KEY=
```

## Security Requirements

- Treat any private key previously committed to repository history as compromised. Rotate the account and move funds before any further use; deleting the current source file does not erase Git history.
- Never reuse `ROVA_SESSION_SECRET`, `ROVA_ENGINE_API_TOKEN`, `ROVA_SIDECAR_TOKEN`, deployer keys, Circle credentials, or Supabase credentials across development, staging, and production.
- Do not expose server-only variables through `NEXT_PUBLIC_*`. The execution-log address is public contract metadata; credentials are not.
- Keep all execution gates `false` until the exact testnet action, amount limit, operator, and rollback procedure are approved.
- An item left in `ready_to_execute` after a transaction or persistence error is intentionally paused. Reconcile its Circle and Arc transaction history before changing its status; never blindly re-arm it.
