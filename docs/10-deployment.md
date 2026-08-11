# 10 - Deployment Guide

FaaSBox is one binary plus the Bun runtime, and one directory that has to
survive a restart. This page starts with what the application needs, whatever
you run it on, then shows the two ways to give it that: a container, which is
the simple path and comes ready-made, or a server you set up yourself.

> **In a hurry?** Jump to [2. Docker](#2-docker-the-simple-path). It is the
> fastest and easiest way to get an instance running — a published image
> already provides everything section 1 describes, and one `docker run` is the
> whole deployment. Section 1 is worth reading afterwards, when you want to
> know what that container is actually doing.

## 1. What FaaSBox needs

### The runtime

Two executables have to be on the host:

- **The FaaSBox binary**, built from `server/` — a single static Go binary, no
  system libraries to install.
- **[Bun](https://bun.sh/)**, which runs your functions. The server spawns
  `bun run` for every invocation and resolves it through **its own `PATH`**, so
  Bun has to be reachable by the account the server runs under.

Your functions themselves see a **rebuilt environment**, never the server's:
`PATH=/usr/local/bin:/usr/bin:/bin`, plus `HOME`, `NODE_ENV` and
`FUNCTION_NAME`, plus that function's own secrets. A command a function shells
out to has to live in one of those three directories, and a variable set on the
server process is invisible to it — that is deliberate, and it is what keeps
`FAASBOX_ENCRYPTION_KEY` and your S3 credentials out of reach of the code you
run.

Nothing else is required. There is no database server, no message broker, no
sidecar.

### The directories

| Directory | What it holds | Must it survive a restart? |
|---|---|---|
| `pb_data/` | The SQLite database: functions, schedules, secrets, keys, logs. | **Yes.** This is the whole instance. |
| `pb_public/` | The built Angular editor, served at `/`. | No — rebuilt from source. |
| `functions/` | One folder per function on disk, plus its `node_modules`. | No — rewritten from the database at startup. |

Two of the three are derived, and that is the point: the database is the source
of truth, and the server rebuilds `functions/` from it on every boot. Losing
that directory costs the time of one dependency install, nothing else.

Where they live:

- `pb_data/` follows the standard PocketBase `--dir` flag.
- `pb_public/` is **not** configurable: the server derives it as a sibling of
  the data directory. `--dir=/app/data/pb_data` means the editor is served from
  `/app/data/pb_public`.
- `functions/` follows `--functionsDir`, which defaults to `./functions`,
  relative to the working directory the server was started in.

### The port

One HTTP port, and nothing else listens.

- Started without `--http`, PocketBase binds **`127.0.0.1:8090`** — loopback
  only, which is what you want behind a reverse proxy on the same host.
- The container image forces **`0.0.0.0:8080`** and declares `EXPOSE 8080`.
- Pass `--http=<address>:<port>` to choose. Everything is served from it: the
  editor at `/`, the PocketBase admin at `/_/`, `/invoke/{name}`, `/functions`
  and the public `/health`.

### Behind a reverse proxy

FaaSBox runs the rate limiter described in
[06 - API Keys & Security](06-api-keys-and-security.md#5-rate-limiting), and it
counts requests **per client IP**. The IP it sees is the one on the TCP
connection. Put a proxy in front — nginx, Caddy, a platform router — and every
request arrives from the proxy, so the limits stop being per-caller and become
global to the instance. On a single-owner instance that difference is mostly
academic; the limit on sign-in attempts still does its job. On one several
people reach, one busy caller spends everybody's budget.

The setting that fixes it is PocketBase's **trusted proxy**, in the admin at
`/_/`: you name the header your proxy writes the real client IP into
(`X-Forwarded-For`, `CF-Connecting-IP`, …).

FaaSBox leaves it empty and offers no environment variable for it, on purpose.
Only you know what your proxy actually rewrites, and naming a header it does
*not* overwrite is **worse than naming none**: callers can then send that header
themselves, pick whatever IP they like, and walk past the limiter entirely. An
empty setting is merely coarse; a wrong one is an open door.

### The environment variables

Only one is genuinely required, and only for a feature you may not use:

| Variable | Status | Without it |
|---|---|---|
| `FAASBOX_ENCRYPTION_KEY` | **Required to use secrets.** 64 hex characters — `openssl rand -hex 32`. | Secrets are disabled with a warning; everything else runs. A malformed value is fatal at startup, on purpose. |
| `SUPERUSER_EMAIL` / `SUPERUSER_PASSWORD` | Recommended for a container. | The step is skipped; create the account at `/_/` on first boot instead. |
| `FAASBOX_PUBLIC_URL` | **Required to authorize an agent by OAuth.** The address this instance answers on, as a bare origin. | The OAuth endpoints are not mounted and a line at startup says why; `/mcp` keeps authenticating by API key. |
| `LITESTREAM_*` | Optional, see below. | No replication; the database stays local. |
| `FAASBOX_MAX_*` | Optional sizing, see below. | Defaults apply. |

> ⚠️ Lose `FAASBOX_ENCRYPTION_KEY` and the secrets encrypted with it are gone.
> Change it, and existing secrets stop being readable — the Environment tab
> answers `500` rather than showing you an empty set you would then overwrite.

#### Telling the instance its own address

`FAASBOX_PUBLIC_URL` is the only place FaaSBox learns what it is called from
outside, and it never guesses. An OAuth authorization server has to publish its
own issuer and the URL of the resource it protects, and a value derived from a
proxy header or from a database setting would put whatever the proxy said — or
`http://localhost:8090` — in a document clients trust.

It has to be a **bare origin**: scheme and host, an optional port, nothing else.
A trailing slash is dropped; a path, a query, a fragment or credentials are
refused. `http://` is accepted on `localhost`, `127.0.0.1` or `::1` only —
anywhere else it is what a reverse proxy wired wrong looks like, and OAuth 2.1
forbids it.

```bash
# Behind a reverse proxy on a domain
FAASBOX_PUBLIC_URL=https://faasbox.example.com

# A droplet reached by its address, TLS terminated in front
FAASBOX_PUBLIC_URL=https://203.0.113.10

# Clever Cloud: the domain of the application, not the internal port it binds
FAASBOX_PUBLIC_URL=https://app-xxxxxxxx.cleverapps.io

# Local development — infra/dev/dev.sh sets this one for you
FAASBOX_PUBLIC_URL=http://127.0.0.1:8080
```

Anything wrong with the value takes the OAuth endpoints down and leaves the rest
of the server running: an agent then connects with an API key, as before. The
startup log names the reason.

#### Sizing an instance

Four size bounds are read at startup and hold until the next restart. Their
defaults suit a general-purpose instance; one that serves large exports, or one
that only supervises, will want different ones.

| Variable | Default | Bounds |
|----------|---------|--------|
| `FAASBOX_MAX_OUTPUT_SIZE` | `1048576` | Bytes captured **per output stream** during a run. Worst case per invocation is twice this, `stdout` plus `stderr`. |
| `FAASBOX_MAX_BODY_SIZE` | `1048576` | Bytes accepted in a request body. Beyond it, `413`. |
| `FAASBOX_MAX_LOG_OUTPUT` | `8192` | Bytes of `stdout`/`stderr` kept per log record. |
| `FAASBOX_MAX_LOG_PAYLOAD` | `4096` | Bytes of `requestPayload` kept per log record. |

An invalid, negative or zero value falls back to the default with a message in
the server log; it never blocks startup. See
[04 - Environment Variables](04-environment-variables.md) for the full list.

`.env.example` at the repository root lists everything with its default.

## 2. Docker (the simple path)

Everything above is already wired in an image published for every release. It
is the recommended way to deploy, and the way the project is deployed itself.

```bash
docker run -d -p 8080:8080 \
  -e SUPERUSER_EMAIL=admin@example.com \
  -e SUPERUSER_PASSWORD=your_secure_password \
  -e FAASBOX_ENCRYPTION_KEY=$(openssl rand -hex 32) \
  -v faasbox-data:/app/data/pb_data \
  ghcr.io/antoineviau/faasbox:latest
```

### Which tag to run

Each release lands under three names on GitHub Container Registry:

| Tag | Follows |
|---|---|
| `:0.1.0` | That exact release, and never moves. |
| `:0.1` | The newest patch of that minor version. |
| `:latest` | The newest release. Prereleases never take it. |

For an instance you intend to keep running, pin `:0.1.0` or `:0.1` and update
on purpose. `:latest` is for trying it out.

### Checking where the image came from

You are being asked to run a binary you did not build, so do not take it on
trust — check it. Every published image carries a **provenance attestation**
signed during the build, which ties it to the commit and the workflow run that
produced it:

```bash
gh attestation verify oci://ghcr.io/antoineviau/faasbox:0.1.0 --repo AntoineViau/FaaSBox
```

A successful verification names the repository, the commit and the workflow.
Be clear on what that buys you: it proves the image is what this repository's
public workflow built from that commit — it says nothing about whether the code
at that commit is any good. It answers *"is this really what I can read in the
repository?"*, not *"is this safe?"*.

### One architecture

The published image is **`linux/amd64` only**. The `Dockerfile` downloads a
Litestream release pinned to `linux-x86_64`, so an `arm64` image would carry a
binary it cannot execute and would fail the moment replication was turned on.
That is why the workflow does not build one — and why building the same
`Dockerfile` on an ARM host would not help you either.

On ARM — an Apple Silicon Mac, a Graviton instance — either run the image
emulated (`docker run --platform linux/amd64 …`, at a cost in speed) or follow
[section 4](#4-manual-deployment-linux-server) and provide the runtime
yourself: the Go binary and Bun both build and install natively there.

### What the image does for you

- **Builds both halves** — the Go binary and the Angular editor — in separate
  stages, so the runtime image carries neither toolchain.
- **Ships Bun** as its base (`oven/bun`), so the function runtime is there and
  its version is pinned along with everything else.
- **Runs as a non-privileged `faasbox` user**, with `tini` as PID 1 so the
  subprocess of every invocation is reaped instead of piling up as a zombie.
- **Creates the three directories** and starts the server on `0.0.0.0:8080`
  with `--dir=/app/data/pb_data --functionsDir=/app/functions`.
- **Declares a healthcheck** that polls `/health` every 30 seconds.

### Building it yourself

The `Dockerfile` the release workflow uses is in the repository, so you can run
the same build locally and skip the registry entirely:

```bash
docker build -f infra/production/Dockerfile -t faasbox .
```

Then use `faasbox` in place of the image name in the commands above.

### Persistence

`-v faasbox-data:/app/data/pb_data` is the one flag you cannot omit. It is the
directory from the table above that has to survive: without it, every restart
starts an empty instance — no functions, no keys, no logs.

The other two directories are deliberately left inside the container. There is
nothing to mount for them, and mounting `functions/` would only pin a cache the
server rebuilds anyway.

## 3. Litestream replication (optional)

For hosts whose filesystem does not survive a redeploy, the image bundles
[Litestream](https://litestream.io/) to replicate the SQLite file continuously
to any S3-compatible storage.

**It is entirely optional.** Leave the variables unset and FaaSBox starts
normally, from the local file alone.

| Variable | Description |
|----------|-------------|
| `LITESTREAM_REPLICA_ENDPOINT` | S3 endpoint (e.g. `s3.amazonaws.com`, `storage.googleapis.com`) |
| `LITESTREAM_REPLICA_BUCKET` | Bucket name — its presence is what turns replication on |
| `LITESTREAM_ACCESS_KEY_ID` | Access key |
| `LITESTREAM_SECRET_ACCESS_KEY` | Secret key |

```bash
docker run -d -p 8080:8080 \
  -e SUPERUSER_EMAIL=admin@example.com \
  -e SUPERUSER_PASSWORD=your_secure_password \
  -e FAASBOX_ENCRYPTION_KEY=$(openssl rand -hex 32) \
  -e LITESTREAM_REPLICA_ENDPOINT=s3.amazonaws.com \
  -e LITESTREAM_REPLICA_BUCKET=my-faasbox-backup \
  -e LITESTREAM_ACCESS_KEY_ID=your_access_key \
  -e LITESTREAM_SECRET_ACCESS_KEY=your_secret_key \
  ghcr.io/antoineviau/faasbox:latest
```

The startup sequence becomes:

1. `litestream restore -if-replica-exists` puts the database back before
   anything reads it. A first boot with no replica passes silently.
2. The superuser is created or updated, if credentials were given.
3. Litestream starts the server as its **child** (`replicate -exec`) and
   mirrors every write as it happens. A clean shutdown of the server triggers a
   last sync.
4. The server rebuilds `functions/` from the database it just restored, then
   reinstalls the dependencies that the fresh filesystem does not have.

Together those two facts are what make the container disposable: the database
comes back from S3, and everything else comes back from the database. The
configuration lives in `infra/production/litestream.yml`.

## 4. Manual deployment (Linux server)

If you would rather not use Docker, you are providing by hand what section 1
lists.

1.  Install **Go 1.25+**, **Node.js 22+** (to build the editor) and **Bun**. Bun
    must be on the `PATH` of the account that will run the server.
2.  Clone the repository.
3.  Build the backend:
    ```bash
    cd server && go build -o ../faasbox .
    ```
4.  Build the editor. It writes to `data/pb_public`, which is where the server
    expects it — a sibling of the data directory:
    ```bash
    cd ui && npm ci && npm run build
    ```
5.  Run the server, choosing the port explicitly:
    ```bash
    export FAASBOX_ENCRYPTION_KEY=your_64_char_hex_string
    ./faasbox serve --http=0.0.0.0:8080 --dir=./data/pb_data
    ```

Put it behind a service manager — systemd or equivalent — so the process is
restarted if it dies. Process reaping is not a concern here: `tini` is in the
image because a container's PID 1 has that duty and the shell entrypoint would
not do it, which is not your situation on a normal init system.

## Backup strategy

Everything is in SQLite, so a backup is a file:

1.  **Snapshot**: copy `data.db` out of `pb_data`. PocketBase can vacuum into a
    backup file.
2.  **PocketBase backups**: the admin UI at `/_/` (`Settings` → `Backups`) zips
    the whole data directory.
3.  **Litestream**: the mirror described above is always current, which is a
    different guarantee from a periodic dump — there is no window during which
    the last hour is missing.

## Updating FaaSBox

1.  Pull the latest code or image.
2.  Rebuild and restart the container or service.
3.  PocketBase applies its own migrations at startup. FaaSBox creates its four
    collections if they are absent — `faasbox_functions`, `faasbox_api_keys`,
    `faasbox_cron_jobs`, `faasbox_logs` — adds missing fields to
    `faasbox_functions` and `faasbox_cron_jobs`, and resizes the `stdout` and
    `stderr` fields of `faasbox_logs` to match the current
    `FAASBOX_MAX_LOG_OUTPUT`
    (see [04 - Environment Variables](04-environment-variables.md)). It never
    removes or renames a field.

    Two more — `faasbox_oauth_clients` and `faasbox_oauth_grants` — are created
    the first time the OAuth endpoints go up, which is to say the first startup
    with a valid `FAASBOX_PUBLIC_URL`. An instance that never sets it never
    carries them.
