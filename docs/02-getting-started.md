# 02 - Getting Started

This guide will help you get your first function running on FaaSBox.

## Prerequisites

- **Docker** (recommended)
- Or **Go 1.24+** and **Bun** (for local development)

## 1. Quick Start with Docker

The fastest way to start is using the official Docker image:

```bash
docker run -d -p 8080:8080 \
  -e SUPERUSER_EMAIL=admin@example.com \
  -e SUPERUSER_PASSWORD=yourpassword \
  -e FAASBOX_ENCRYPTION_KEY=$(openssl rand -hex 32) \
  -v faasbox-data:/app/data/pb_data \
  faasbox
```

### Environment Variables:
- `SUPERUSER_EMAIL`: The email for your admin account.
- `SUPERUSER_PASSWORD`: The password for your admin account (min 8 chars).
- `FAASBOX_ENCRYPTION_KEY`: A 64-character hex string used to encrypt secrets. **Keep this safe!**
- `-v faasbox-data:/app/data/pb_data`: Mounts a volume to persist your data.

## 2. Local Development (without Docker)

Requires **Go 1.24+**, **Bun**, and **Node.js** (for the Angular build).

The quickest way is the all-in-one dev script:

```bash
bash infra/dev/dev.sh
```

You can optionally pass superuser credentials to skip the manual creation step:

```bash
SUPERUSER_EMAIL=admin@example.com SUPERUSER_PASSWORD=changeme bash infra/dev/dev.sh
```

Or step by step:

```bash
# Build the Angular editor
cd ui && npm install && npm run build && cd ..

# Start the server (generates an encryption key if not set)
export FAASBOX_ENCRYPTION_KEY=$(openssl rand -hex 32)
cd server && go run . serve --http=127.0.0.1:8080 --dir=../data/pb_data
```

On first launch, open `http://localhost:8080/_/` to create your superuser account.

## 3. Access the Dashboards

Once started, you have access to two main interfaces:

1.  **PocketBase Admin (`/_/`)**: `http://localhost:8080/_/`
    - Manage Cron jobs and view logs.
    - View the underlying data collections.
2.  **FaaS Editor (`/`)**: `http://localhost:8080/`
    - Write, edit, and test your functions in a web-based IDE.
    - Run functions manually and see the output in real-time.

## 4. Create Your First Function

1.  Open the **FaaS Editor**.
2.  Click "New Function" (or create a folder in `functions/` if developing locally).
3.  Name it `hello-world`.
4.  In `index.ts`, write the following:

```typescript
// Read input from stdin
const input = await Bun.stdin.text();
const data = JSON.parse(input || "{}");

// Perform logic
const name = data.name || "World";

// Return output via stdout
console.log(JSON.stringify({
  message: `Hello, ${name}!`,
  timestamp: new Date().toISOString()
}));
```

## 5. Invoke Your Function

### Step 1: Create an API Key
Go to the **PocketBase Admin UI** -> **faasbox_api_keys** collection. **IMPORTANT**: Creating a record manually in the Admin UI will not show you the raw API key. To generate a new key and see it, you must use the API:

```bash
# Get a superuser token first
TOKEN=$(curl -s -X POST http://localhost:8080/api/collections/_superusers/auth-with-password \
  -H 'Content-Type: application/json' \
  -d '{"identity":"admin@example.com","password":"yourpassword"}' | jq -r '.token')

# Create the key
curl -s -X POST http://localhost:8080/api/faasbox/keys \
  -H "Authorization: $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Dev Key"}'
```

The response will contain the `key` (starting with `fbx_`). Copy it.

### Step 2: Call via Curl
```bash
curl -X POST http://localhost:8080/invoke/hello-world \
  -H "X-API-Key: YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name": "Developer"}'
```

You should receive a JSON response:
```json
{
  "function": "hello-world",
  "result": {
    "message": "Hello, Developer!",
    "timestamp": "2026-03-01T..."
  },
  "duration_ms": 15
}
```

## Next Steps
- Learn how to [manage dependencies](03-writing-functions.md).
- Secure your function with [encrypted environment variables](04-environment-variables.md).
- Schedule your function with [Cron jobs](05-scheduling-cron.md).
