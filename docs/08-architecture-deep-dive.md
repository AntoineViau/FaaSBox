# 08 - Architecture Deep Dive

This document explains the technical implementation of FaaSBox for developers and curious users.

## The Stack

- **Language**: [Go](https://go.dev/) (for the server)
- **Framework**: [PocketBase](https://pocketbase.io/) (Auth, DB, Router, UI)
- **Runtime**: [Bun](https://bun.sh/) (Function execution)
- **Frontend**: [Angular](https://angular.dev/) (Editor & Runner)
- **Database**: [SQLite](https://www.sqlite.org/) (Embedded via PocketBase)

FaaSBox integrates all of it into a single binary, shipped as a single container:

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

## Component Overview

### 1. The Go Server (`faasbox`)
The Go binary is the orchestrator. It extends PocketBase with custom routes and hooks:
- **API Key Middleware**: Validates the `X-API-Key` header by hashing the input and comparing it with stored hashes.
- **Execution Engine**: Manages the spawning of Bun subprocesses, pipes stdin/stdout, and handles timeouts.
- **Cron Sync**: A background task that syncs DB records with the internal Go cron scheduler.
- **Secret Manager**: Handles AES-256-GCM encryption/decryption of environment variables.

### 2. The Execution Flow
When `/invoke/{name}` is called:
1.  **Auth**: Middleware checks the API key.
2.  **Lookup**: Server verifies that `functions/{name}/index.ts` exists.
3.  **Dependencies**: `deps.go` checks whether `node_modules` matches the current spec. It normally does — the install already ran when the function was saved — so this is a fingerprint comparison and nothing more.
4.  **Secrets**: Decrypts any secrets configured for this function.
5.  **Spawn**: Starts `bun run functions/{name}/index.ts`.
6.  **Pipe**: Writes the HTTP request body to the subprocess's `stdin`.
7.  **Monitor**: Waits for the process to finish or hit a 30s timeout. On timeout, the entire process group is killed (not just the main process), preventing zombie subprocesses.
8.  **Record**: Saves the result, duration, and logs to `faasbox_logs`.
9.  **Respond**: Returns the JSON result to the client.

### 3. Dependency Installation
Installing happens when a function is **saved**, in the background: the save returns straight away, and the `depsStatus` field on the record reports where the install stands.

**At startup**, a second background pass covers what a save cannot see: `node_modules` is not part of what the database restores, so a rebuilt filesystem has none. The pass walks the functions, skips those whose fingerprint already matches, and installs the rest one at a time — serialised, because installing them all at once would multiply the memory peak by their number. It is detached from startup, so the server listens without waiting for it.

The invocation path keeps its own check as a last resort, for what both miss — a `node_modules` removed by hand, or a startup install that failed.

To prevent race conditions where a background install and an invocation try to run `bun install` at the same time, we use a global Go `sync.Map` of mutexes. One mutex per function directory: the second caller waits for the first, then finds the work already done.

### 4. Output Truncation
Truncation happens twice, for two different reasons.

- **To protect the server's memory**, a `LimitedWriter` captures only the first 1 MB of `stdout` and `stderr` — per stream, and adjustable via `FAASBOX_MAX_OUTPUT_SIZE`. Writes past that point are discarded and the response carries a `truncated` flag.
- **To protect the database**, the copy written to `faasbox_logs` is cut much lower — 8 KB per stream and 4 KB for the request payload — with a marker stating the original size. Without this second cap, a single log row could weigh 21 MB, and SQLite never gives disk back once the file has grown.

The HTTP response is built from the captured output, not from the log record, so it always carries the full capture.

The first cap has a consequence the flag alone does not convey: when the cut lands mid-JSON, the surviving fragment is not a result. The invocation path tracks `stdout` truncation separately from the combined flag and refuses that case with a `502` rather than returning the fragment as a plain string. The cron path does not parse output at all, so it keeps recording whatever it captured.

### 5. The Built-in Editor
The editor is a standalone Angular Single Page Application (SPA).
- **Location**: Built into `data/pb_public/`.
- **Communication**: It uses the PocketBase SDK and custom FaaS endpoints.
- **Features**: Code editing (via CodeMirror), real-time invocation, and log viewing.

## Database-to-Disk Synchronization

The **database is the source of truth** for function code. The `faasbox_functions` collection stores each function's `script` (the `index.ts` content) and `packageJson`. The file system is a derived copy, rebuilt automatically.

### Startup Sync

When the server starts, `syncDiskFromDB` reads every record from `faasbox_functions` and writes the corresponding `index.ts` (and `package.json` and `bun.lock` if present) to the `functions/` directory. This ensures the file system is always consistent after a container restart or redeployment — even if the `functions/` directory was empty. The dependency pass described above then runs on the files it just restored.

### Live Sync via Hooks

PocketBase hooks keep the disk in sync in real time as records change:

- **Create / Update** (`OnRecordAfterCreateSuccess`, `OnRecordAfterUpdateSuccess`): writes the function files to disk immediately.
- **Delete** (`OnRecordAfterDeleteSuccess`): removes the function directory from disk.

This means you never need to manage files manually. Whether you edit code in the Editor, create a function via the API, or modify a record in the Admin UI, the disk is updated instantly.

## File System Structure (Inside Container)

```text
/app/
├── faasbox          # The compiled Go binary
├── functions/           # The folder containing your TypeScript code
│   └── my-func/
│       ├── index.ts
│       └── package.json
└── data/
    ├── pb_data/         # Persistent SQLite database & settings
    └── pb_public/       # Static files for the Editor (the Admin UI is embedded in the binary)
```

## Compilation & Build
The Go binary is compiled with hardening flags:
- `-trimpath`: Removes local file paths from the binary.
- `-ldflags="-s -w"`: Strips debug information and symbol tables.
- `CGO_ENABLED=0`: Produces a static binary for maximum portability.
