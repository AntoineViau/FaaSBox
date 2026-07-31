# 🚀 FaaSBox

**FaaSBox** is a lightweight, self-hosted **Functions-as-a-Service** platform built on top of [PocketBase](https://pocketbase.io/) and [Bun](https://bun.sh/). 

It allows you to write TypeScript/JavaScript functions, deploy them instantly, invoke them via HTTP with hashed API keys, or schedule them using a flexible cron system. Code, dependencies, secrets, schedules and execution logs are all managed from the built-in editor, with the PocketBase admin UI underneath for direct access to the underlying collections.

---

## 🏗️ Architecture

FaaSBox integrates several powerful technologies into a single binary/container:

- **PocketBase (Go)**: The backbone. Handles HTTP routing, SQLite persistence, Authentication, Cron scheduling, and the Admin UI.
- **Bun**: The high-performance JavaScript runtime. Executes functions in isolated subprocesses with fast startup times.
- **Angular (UI)**: A modern, signal-based frontend for managing and testing functions directly from your browser.

```mermaid
graph TD
    User([User / App]) -->|HTTP / API Key| PB[PocketBase Server]
    Cron[Cron Scheduler] -->|Internal Trigger| PB
    
    subgraph "Execution Engine"
        PB -->|Spawn Subprocess| Bun[Bun Runtime]
        Bun -->|Read| Input[Stdin JSON]
        Bun -->|Write| Output[Stdout JSON]
        Bun -->|Log| Error[Stderr]
    end
    
    subgraph "Persistence (SQLite)"
        PB --- Logs[(Execution Logs)]
        PB --- Keys[(Hashed API Keys)]
        PB --- Config[(Encrypted Secrets)]
        PB --- Functions[(Function Code Sync)]
    end
```

---

## ⚡ Quick Start

### 1. Run with Docker
```bash
docker build -f infra/production/Dockerfile -t faasbox .

docker run -d -p 8080:8080 \
  -e SUPERUSER_EMAIL=admin@example.com \
  -e SUPERUSER_PASSWORD=yourpassword \
  -e FAASBOX_ENCRYPTION_KEY=$(openssl rand -hex 32) \
  -e FAASBOX_MAX_CONCURRENCY=4 \
  -v faasbox-data:/app/data/pb_data \
  faasbox
```

### 2. Run locally (without Docker)

Requires **Go 1.24+**, **Bun**, and **Node.js** (for the Angular build).

```bash
# All-in-one: builds the UI then starts the Go server
bash infra/dev/dev.sh
```

Or step by step:

```bash
# Build the Angular editor
cd ui && npm install && npm run build && cd ..

# Start the server (generates an encryption key if not set)
export FAASBOX_ENCRYPTION_KEY=$(openssl rand -hex 32)
cd server && go run . serve --http=127.0.0.1:8080 --dir=../data/pb_data
```

Then open `http://localhost:8080/_/` to create your superuser account on first launch.

### 3. Access the UI
- **Admin UI**: `http://localhost:8080/_/` (for system config, logs, and keys).
- **Editor UI**: `http://localhost:8080/` (to write and test functions).

### 4. Create an API Key
```bash
# 1. Get a token
TOKEN=$(curl -s -X POST http://localhost:8080/api/collections/_superusers/auth-with-password \
  -H 'Content-Type: application/json' \
  -d '{"identity":"admin@example.com","password":"yourpassword"}' | jq -r '.token')

# 2. Create a key
curl -s -X POST http://localhost:8080/api/faasbox/keys \
  -H "Authorization: $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"my-app","allowedFunctions":["*"]}'
```

### 5. Invoke a Function
```bash
curl -X POST http://localhost:8080/invoke/echo \
  -H "X-API-Key: YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"hello": "world"}'
```

---

## 🔒 Security Highlights

- **Hashed API Keys**: Only SHA-256 hashes of API keys are stored.
- **Encrypted Secrets**: Custom env vars are encrypted at rest using AES-256-GCM.
- **Isolated Runtimes**: Functions run in ephemeral subprocesses with minimal environments.
- **Non-Root Execution**: The Docker container runs as a dedicated `faas` user.

---

## 📑 Full Documentation

- [**00 - Introduction**](docs/00-introduction.md)
- [**01 - Concepts**](docs/01-concepts.md)
- [**02 - Getting Started**](docs/02-getting-started.md)
- [**03 - Writing Functions**](docs/03-writing-functions.md)
- [**04 - Environment Variables**](docs/04-environment-variables.md)
- [**05 - Scheduling (Cron)**](docs/05-scheduling-cron.md)
- [**06 - API Keys & Security**](docs/06-api-keys-and-security.md)
- [**07 - Execution Logs**](docs/07-execution-logs.md)
- [**08 - Architecture Deep Dive**](docs/08-architecture-deep-dive.md)
- [**09 - API Reference**](docs/09-api-reference.md)
- [**10 - Deployment**](docs/10-deployment.md)
- [**11 - Development Guide**](docs/11-development-guide.md)

---

Built with ❤️ using **Go**, **PocketBase**, **Bun**, and **Angular**.
