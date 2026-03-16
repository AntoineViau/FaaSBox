# 08 - Architecture Deep Dive

This document explains the technical implementation of FaaSBox for developers and curious users.

## The Stack

- **Language**: [Go](https://go.dev/) (for the server)
- **Framework**: [PocketBase](https://pocketbase.io/) (Auth, DB, Router, UI)
- **Runtime**: [Bun](https://bun.sh/) (Function execution)
- **Frontend**: [Angular](https://angular.dev/) (Editor & Runner)
- **Database**: [SQLite](https://www.sqlite.org/) (Embedded via PocketBase)

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
3.  **Dependencies**: `deps.go` checks if `bun install` is needed.
4.  **Secrets**: Decrypts any secrets configured for this function.
5.  **Spawn**: Starts `bun run functions/{name}/index.ts`.
6.  **Pipe**: Writes the HTTP request body to the subprocess's `stdin`.
7.  **Monitor**: Waits for the process to finish or hit a 30s timeout. On timeout, the entire process group is killed (not just the main process), preventing zombie subprocesses.
8.  **Record**: Saves the result, duration, and logs to `faasbox_logs`.
9.  **Respond**: Returns the JSON result to the client.

### 3. The Dependency Mutex
To prevent race conditions where multiple requests try to run `bun install` at the same time, we use a global Go `sync.Map` of mutexes. One mutex per function.

### 4. Output Truncation
To protect the server's memory and the database, we use a `LimitedWriter` to capture only the first 10 MB of `stdout` and `stderr`. If a function outputs more, it is truncated.

### 5. The Built-in Editor
The editor is a standalone Angular Single Page Application (SPA).
- **Location**: Built into `data/pb_public/`.
- **Communication**: It uses the PocketBase SDK and custom FaaS endpoints.
- **Features**: Code editing (via CodeMirror), real-time invocation, and log viewing.

## Database-to-Disk Synchronization

The **database is the source of truth** for function code. The `faasbox_functions` collection stores each function's `script` (the `index.ts` content) and `packageJson`. The file system is a derived copy, rebuilt automatically.

### Startup Sync

When the server starts, `syncDiskFromDB` reads every record from `faasbox_functions` and writes the corresponding `index.ts` (and `package.json` if present) to the `functions/` directory. This ensures the file system is always consistent after a container restart or redeployment — even if the `functions/` directory was empty.

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
    └── pb_public/       # Static files for the Admin UI and Editor
```

## Compilation & Build
The Go binary is compiled with hardening flags:
- `-trimpath`: Removes local file paths from the binary.
- `-ldflags="-s -w"`: Strips debug information and symbol tables.
- `CGO_ENABLED=0`: Produces a static binary for maximum portability.
