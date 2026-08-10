# 02 - Getting Started

This guide will help you get your first function running on FaaSBox.

## Prerequisites

- **Docker** (recommended)
- Or **Go 1.24+** and **Bun** (for local development)

## 1. Quick Start with Docker

The fastest way to start is the published image. There is nothing to clone and
nothing to build:

```bash
docker run -d -p 8080:8080 \
  -e SUPERUSER_EMAIL=admin@example.com \
  -e SUPERUSER_PASSWORD=yourpassword \
  -e FAASBOX_ENCRYPTION_KEY=$(openssl rand -hex 32) \
  -v faasbox-data:/app/data/pb_data \
  ghcr.io/antoineviau/faasbox:latest
```

`latest` follows the newest release. Pin a version — `:0.1.0`, or `:0.1` to
follow its patches — for anything you intend to keep running. The image is
built for `linux/amd64` only; see
[10 - Deployment](10-deployment.md#2-docker-the-simple-path) for that, and for
how to check where the image came from before you run it.

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

If you started the server without superuser credentials, open `http://localhost:8080/_/` once to create the account. Both the Docker command above and `dev.sh` with `SUPERUSER_EMAIL` and `SUPERUSER_PASSWORD` already create it for you — sign in with those.

## 3. The Two Interfaces

### FaaS Editor (`/`) — where you work

`http://localhost:8080/` is the interface you will spend your time in. It covers everything you need to run functions:

- **Write and test.** Five tabs per function — **Script**, **package.json**, **Triggers**, **Environment**, **Files** — plus a **Runner** to invoke a function on demand and a **Logs** panel to read its history. Both open from the buttons in the header.
- **Schedule.** Cron triggers are created and edited in the **Triggers** tab, with ready-made expressions and a free field. See [05 - Scheduling (Cron)](05-scheduling-cron.md).
- **Secrets.** Encrypted environment variables are edited as key/value pairs in the **Environment** tab. See [04 - Environment Variables](04-environment-variables.md).
- **Look at the disk.** The **Files** tab browses the function's own folder as it exists on the server — `index.ts`, `package.json`, `bun.lock`, `node_modules` and whatever else is in there. Read-only. See [03 - Writing Functions](03-writing-functions.md).
- **API keys.** A dedicated page, reached from **API keys** at the top of the left sidebar, creates, scopes, disables and deletes keys. See [06 - API Keys and Security](06-api-keys-and-security.md).
- **Share what you are looking at.** The address bar names the open function and the open tab — `/editor/<id>/tab/script` — so a link can be bookmarked or sent to someone, a reload comes back exactly where you left off, and the browser Back button walks back through the functions and tabs you visited. The link carries the function's id rather than its name, so it survives a rename.
- **Theme.** The sun/moon button in the header switches between light and dark. Your choice is remembered by the browser; without one, the editor follows your system preference.

#### Keyboard shortcuts

These work in the **Script** and **package.json** code panels. On macOS, use `Cmd` wherever the table says `Ctrl` — except for `Ctrl+Space`, which is `Ctrl` on every platform, and for the row that names its own macOS key.

| Shortcut                          | What it does                                                                    |
| --------------------------------- | ------------------------------------------------------------------------------- |
| `Tab` / `Shift+Tab`               | Indent or unindent the current line or selection, by two spaces.                |
| `Tab` (completion open)           | Accept the highlighted proposal instead of indenting.                           |
| `Ctrl+Space`                      | Open the completion popup.                                                      |
| `Ctrl+/`                          | Comment or uncomment every line of the selection.                               |
| `Shift+Alt+A`                     | Wrap the selection in a block comment. See the macOS note below.                |
| `Ctrl+F`                          | Open the search panel.                                                          |
| `Ctrl+Shift+M`                    | Open the diagnostics panel — where a `package.json` error is spelled out.       |
| `Ctrl+S`                          | Save the function.                                                              |
| `Ctrl+M` (`Shift+Alt+M` on macOS) | Hand `Tab` back to keyboard navigation. Press it again to get indentation back. |

That last one is not a curiosity. Because `Tab` indents, it no longer moves focus out of the code panel — so if you navigate with the keyboard, `Ctrl+M` is how you leave the editor. It toggles: the setting stays until you press it again.

**Block comments on macOS.** `Shift+Alt+A` does not reach the editor there. `Option` composes characters on macOS, so the browser reports `Å` instead of `A` and the binding never matches — CodeMirror skips its usual fallback for `Alt` combinations on that platform, on purpose. Use `Cmd+/` over the selected lines instead; it comments the same block, one `//` per line.

### PocketBase Admin (`/_/`) — the collections underneath

`http://localhost:8080/_/` is the standard PocketBase dashboard. You no longer need it for day-to-day work; it is there when you want the raw data:

- Browse and query the four collections directly (`faasbox_functions`, `faasbox_cron_jobs`, `faasbox_logs`, `faasbox_api_keys`).
- Read fields the editor does not surface yet, such as `depsStatus` and `depsError`.
- Manage the instance itself: superuser accounts, backups, server settings.

One thing it cannot do: **creating an API key record by hand gives you no usable key**, because only a hash is stored. Use the editor or the API — see step 5.

## 4. Create Your First Function

1.  Open the **FaaS Editor**.
2.  Click the **+** button next to *Functions* in the left sidebar. The reload button beside it re-reads the list from the server — the sidebar is filled when the page loads, so a function created from the [management API](09-api-reference.md), from the PocketBase admin UI or in another tab appears when you ask for it.
3.  Name it `hello-world`.
4.  In the **Script** tab, write the following:

```typescript
// Read payload from stdin
const payload = await Bun.stdin.text();
const data = JSON.parse(payload || "{}");

// Perform logic
const name = data.name || "World";

// Return output via stdout
console.log(
  JSON.stringify({
    message: `Hello, ${name}!`,
    timestamp: new Date().toISOString(),
  }),
);
```

## 5. Invoke Your Function

### Step 1: Create an API Key

The quickest way is the editor: click **API keys** at the top of the left sidebar and fill the creation form — the raw key is revealed once, with a copy button. See [06 - API Keys and Security](06-api-keys-and-security.md).

By hand, use the API — not the PocketBase Admin UI, for the reason given in step 3: only a hash of the key is stored, so a record you fill in yourself matches no key you can present.

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
