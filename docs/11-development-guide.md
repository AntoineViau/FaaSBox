# 11 - Development Guide

Welcome! This guide explains how to set up your local environment to contribute to FaaSBox.

## Project Structure

```text
.
├── server/             # Go Backend (PocketBase extension)
├── ui/                 # Angular Frontend (FaaS Editor)
├── functions/          # Example/Default Functions
├── infra/              # Deployment & Dev scripts
├── data/
│   ├── pb_data/        # SQLite DB & Settings (Local dev)
│   └── pb_public/      # Static files (Frontend build)
└── docs/               # Documentation
```

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
npm run build  # This outputs to ../data/pb_public/faasbox
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
The backend is a single package in `server/`.
- `main.go`: Entry point, route registration, and app bootstrap.
- `exec.go`: Logic for spawning Bun processes.
- `apikeys.go`: Middleware and hashing logic.
- `cron.go`: Synchronization with the cron scheduler.
- `crypto.go`: AES encryption logic.

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
