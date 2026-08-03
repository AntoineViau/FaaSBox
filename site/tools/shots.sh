#!/bin/bash
#
# Regenerates the editor screenshots end to end: builds the UI, starts a
# throwaway FaaSBox on a disposable database, fills it with demo content,
# photographs it in both themes, and installs the PNGs in ../assets/shots/.
#
#   bash site/tools/shots.sh
#
# Needs Go, Bun, Node 23+, google-chrome and ImageMagick. Nothing it touches
# outlives the run except the screenshots — your own data/pb_data and
# server/functions are never opened.

set -euo pipefail
cd "$(dirname "$0")/../.."

PORT="${PORT:-8099}"
WORK="$(mktemp -d)"
EMAIL="shots@faasbox.local"
PASSWORD="shotspassword123"

# A fixed key: the database is thrown away, so there is nothing to protect.
export FAASBOX_ENCRYPTION_KEY=4966dd3de23c98e13aa7907e00cb4c46b795ba1bd52e4602445308add9002b9d

cleanup() {
  [ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

# Fail here rather than several steps later with a confusing error: a busy port
# lets the server exit while the capture goes on talking to whatever answers.
if (echo >"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null; then
  echo "Port $PORT is already in use. Stop what is listening, or set PORT=…" >&2
  exit 1
fi

echo "▸ Building the editor"
(cd ui && npm run build >/dev/null)

echo "▸ Building the server"
(cd server && go build -o "$WORK/faasbox" .)

# publicDir is derived as a sibling of the data dir, so the built UI has to sit
# next to the throwaway database rather than in the repo's data/ directory.
ln -s "$PWD/data/pb_public" "$WORK/pb_public"
mkdir -p "$WORK/functions"

echo "▸ Creating the superuser"
"$WORK/faasbox" superuser upsert "$EMAIL" "$PASSWORD" --dir="$WORK/pb_data" >/dev/null

echo "▸ Starting FaaSBox on :$PORT"
"$WORK/faasbox" serve \
  --http="127.0.0.1:$PORT" \
  --dir="$WORK/pb_data" \
  --functionsDir="$WORK/functions" > "$WORK/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 40); do
  curl -sf "http://127.0.0.1:$PORT/health" >/dev/null && break
  sleep 0.25
done
curl -sf "http://127.0.0.1:$PORT/health" >/dev/null || { cat "$WORK/server.log"; exit 1; }

echo "▸ Seeding demo content"
node site/tools/seed.mjs --url "http://127.0.0.1:$PORT"

echo "▸ Capturing"
node site/tools/capture.mjs --url "http://127.0.0.1:$PORT"

echo "▸ Done. Rebuild the site to pick them up:  cd site && node build.mjs"
