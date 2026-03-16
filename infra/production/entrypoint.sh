#!/bin/sh
set -e

DATA_DIR=/app/data/pb_data
DB_PATH="$DATA_DIR/data.db"

mkdir -p "$DATA_DIR"

# Restore DB from S3 (skip silently if no backup exists yet)
if [ -n "$LITESTREAM_REPLICA_BUCKET" ]; then
  litestream restore -if-replica-exists -config /etc/litestream.yml "$DB_PATH"
fi

# Create/update superuser
if [ -n "$SUPERUSER_EMAIL" ] && [ -n "$SUPERUSER_PASSWORD" ]; then
  if ! /app/faasbox superuser upsert "$SUPERUSER_EMAIL" "$SUPERUSER_PASSWORD" --dir="$DATA_DIR" 2>&1; then
    echo "WARNING: superuser upsert failed" >&2
  fi
fi

# Start with Litestream replication (or without if not configured)
if [ -n "$LITESTREAM_REPLICA_BUCKET" ]; then
  exec litestream replicate -exec "/app/faasbox serve --http=0.0.0.0:8080 --dir=$DATA_DIR --functionsDir=/app/functions" -config /etc/litestream.yml
else
  exec /app/faasbox serve --http=0.0.0.0:8080 --dir="$DATA_DIR" --functionsDir=/app/functions
fi
