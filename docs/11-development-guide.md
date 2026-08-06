# 11 - Development Guide

Welcome! This guide explains how to set up your local environment to contribute to FaaSBox.

## Project Structure

```text
.
├── server/             # Go Backend (PocketBase extension)
│   └── functions/      # Function folders on disk (rebuilt from the database)
├── ui/                 # Angular Frontend (FaaS Editor)
├── site/               # faasbox.net landing page (static generator)
├── infra/              # Deployment & Dev scripts
├── data/
│   ├── pb_data/        # SQLite DB & Settings (Local dev)
│   └── pb_public/      # Static files (Frontend build)
└── docs/               # Documentation
```

The `functions/` directory is a cache, not a source: the database holds the code
and rewrites the files at startup. Its location follows the `--functionsDir`
flag, which defaults to `./functions` relative to where the server was started.

## Prerequisites
- **Go 1.24+**
- **Node.js 22+** & **npm**
- **Bun** (required to run functions)

## Local Development Setup

### 1. Build the Frontend
The frontend needs to be built so the Go server can serve it.

```bash
cd ui
npm install
npm run build  # This outputs to ../data/pb_public
```

### 2. Run the Backend
You can run the Go server in "serve" mode.

```bash
cd server
go run . serve --http=0.0.0.0:8080 --dir=../data/pb_data
```

### 3. Use the Dev Script
We provide a helper script that builds the UI and starts the server:
```bash
bash infra/dev/dev.sh
```

You can pass superuser credentials via environment variables:
```bash
SUPERUSER_EMAIL=admin@example.com SUPERUSER_PASSWORD=changeme bash infra/dev/dev.sh
```

## Backend Development (Go)
The backend is a single `main` package in `server/`, split one file per domain.
No sub-packages: the tests live in the same package and reach unexported
identifiers without ceremony.

- `main.go`: Entry point, hooks, route registration, and app bootstrap.
- `exec.go`: The shared execution engine — spawning and bounding Bun processes.
- `invoke.go`: The HTTP path, request body limit and concurrency semaphore.
- `cron.go` / `cronmissed.go`: The scheduled path, and the report of the runs that came due while the server was down.
- `deps.go` / `depsstate.go` / `depsstartup.go`: Running `bun install`, recording its outcome on the record, and reinstalling at startup.
- `functions.go` / `files.go`: The functions collection and disk sync, and the read-only routes that browse a function's folder.
- `apikeys.go`: Middleware, hashing and scope enforcement.
- `crypto.go`: AES-256-GCM encryption of the secrets.
- `logs.go`: Writing and pruning execution logs.
- `realtime.go`: Pushing the dependency state to subscribed clients.
- `limitedwriter.go` / `config.go`: Bounded output capture, and reading tunable bounds from the environment.

### Running Tests
```bash
cd server
go test -v .
```

## Frontend Development (Angular)
The UI is a modern Angular app using **Signals** and **Tailwind CSS v4**.

- **Run Dev Server**: `cd ui && npm start` (proxy configured to `:8080`).
- **Style Guide**: We use **Signals** for state management and **ZardUI** for components.
- **Single File Components**: We prefer keeping templates and styles inline in the `.component.ts` file for simplicity.

## Adding a New Feature
1.  **Database**: If you need a new collection, add the schema initialization in `server/main.go` within the `OnServe` hook using the `ensure...Collection` pattern.
2.  **Logic**: Implement the Go logic. Add tests in a `_test.go` file.
3.  **UI**: Update the Angular app to expose the new feature.
4.  **Docs**: Update the relevant files in `docs/`.

## Coding Standards
- **Go**: Follow `go fmt` and standard idiomatic Go patterns.
- **TypeScript**: Use strict typing and Angular Signals. Avoid RxJS `Observables` unless absolutely necessary.
- **Security**: Never log sensitive data. Always validate user input.
