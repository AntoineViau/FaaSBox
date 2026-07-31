# 10 - Deployment Guide

FaaSBox is designed to be easy to deploy. It's essentially a stateless application (the binary/container) with a single stateful requirement: the `pb_data` directory.

## 1. Docker Deployment (Recommended)

The easiest way to deploy is using Docker.

```bash
docker run -d -p 8080:8080 \
  -e SUPERUSER_EMAIL=admin@example.com \
  -e SUPERUSER_PASSWORD=your_secure_password \
  -e FAASBOX_ENCRYPTION_KEY=$(openssl rand -hex 32) \
  -v faasbox-data:/app/data/pb_data \
  faasbox
```

### Persistence
The `-v faasbox-data:/app/data/pb_data` flag is critical. It creates a Docker volume that stores your SQLite database, settings, and uploaded files. Without this, you will lose your API keys and logs every time the container restarts.

### Sizing an Instance

Four size bounds are set at startup and hold until the next restart. Their defaults suit a general-purpose instance; an instance that serves large exports, or one that only supervises, will want different ones.

| Variable | Default | Bounds |
|----------|---------|--------|
| `FAASBOX_MAX_OUTPUT_SIZE` | `1048576` | Bytes captured **per output stream** during a run. Worst case per invocation is twice this, `stdout` plus `stderr`. |
| `FAASBOX_MAX_BODY_SIZE` | `1048576` | Bytes accepted in a request body. Beyond it, `413`. |
| `FAASBOX_MAX_LOG_OUTPUT` | `8192` | Bytes of `stdout`/`stderr` kept per log record. |
| `FAASBOX_MAX_LOG_PAYLOAD` | `4096` | Bytes of `requestPayload` kept per log record. |

An invalid, negative or zero value falls back to the default with a message in the server log; it never blocks startup. See [04 - Environment Variables](04-environment-variables.md) for the full list of server settings and for the caveat on raising `FAASBOX_MAX_LOG_OUTPUT` against an existing database.

## 2. Litestream Replication (Optional)

For ephemeral environments (containers that lose their filesystem on restart), FaaSBox includes [Litestream](https://litestream.io/) to continuously replicate the SQLite database to any S3-compatible storage.

**Litestream is entirely optional.** If the environment variables below are not set, FaaSBox starts normally without replication.

### How it works

1. On startup, Litestream restores the database from S3 (skipped if no backup exists yet).
2. FaaSBox starts and Litestream continuously replicates changes in the background.
3. Function code is stored in the database and restored to disk automatically on boot via `syncDiskFromDB`.

### Configuration

Set these environment variables to enable replication:

| Variable | Description |
|----------|-------------|
| `LITESTREAM_REPLICA_ENDPOINT` | S3 endpoint (e.g. `s3.amazonaws.com`, `storage.googleapis.com`) |
| `LITESTREAM_REPLICA_BUCKET` | S3 bucket name |
| `LITESTREAM_ACCESS_KEY_ID` | S3 access key |
| `LITESTREAM_SECRET_ACCESS_KEY` | S3 secret key |

```bash
docker run -d -p 8080:8080 \
  -e SUPERUSER_EMAIL=admin@example.com \
  -e SUPERUSER_PASSWORD=your_secure_password \
  -e FAASBOX_ENCRYPTION_KEY=$(openssl rand -hex 32) \
  -e LITESTREAM_REPLICA_ENDPOINT=s3.amazonaws.com \
  -e LITESTREAM_REPLICA_BUCKET=my-faasbox-backup \
  -e LITESTREAM_ACCESS_KEY_ID=your_access_key \
  -e LITESTREAM_SECRET_ACCESS_KEY=your_secret_key \
  faasbox
```

The Litestream configuration is in `infra/production/litestream.yml`.

## 3. Manual Deployment (Linux Server)

If you prefer not to use Docker:

1.  Install **Go 1.24+** and **Bun**.
2.  Clone the repository.
3.  Build the backend:
    ```bash
    cd server && go build -o ../faasbox .
    ```
4.  Build the frontend:
    ```bash
    cd ui && npm install && npm run build
    ```
5.  Run the server:
    ```bash
    ./faasbox serve --http=0.0.0.0:8080 --dir=./data/pb_data
    ```

## Backup Strategy

Since everything is in SQLite, backups are easy:

1.  **Snapshot**: Copy the `data.db` file from `pb_data`. PocketBase supports vacuum into a backup file.
2.  **PocketBase Backups**: You can use the built-in backup system in the Admin UI (`Settings` -> `Backups`) to create zip archives of your entire data directory.

## Updating FaaSBox

To update to a new version:
1.  Pull the latest code/image.
2.  Restart the container/service.
3.  PocketBase will automatically handle any database migrations on startup.
