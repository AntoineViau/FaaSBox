#!/bin/bash

# Stop on error
set -e
cd "$(dirname "$0")/../.."

echo "🚀 Preparing local FaaSBox environment..."

# 1. Install UI dependencies
if [ ! -d "ui/node_modules" ]; then
    echo "📦 Installing Angular dependencies..."
    cd ui && npm install && cd ..
fi

# 2. Encryption key. It is kept next to the database it decrypts: a key minted
#    fresh on every launch would leave every secret written by the previous run
#    permanently unreadable, and the editor would answer 500 on its Environment
#    tab with no way back. Same directory means same lifetime — wiping pb_data
#    to reset the schema drops the key with the ciphertext it opens.
DEV_KEY_FILE="data/pb_data/.faasbox-dev-key"
if [ -n "$FAASBOX_ENCRYPTION_KEY" ]; then
    echo "🔑 Using FAASBOX_ENCRYPTION_KEY from the environment."
elif [ -s "$DEV_KEY_FILE" ]; then
    export FAASBOX_ENCRYPTION_KEY=$(cat "$DEV_KEY_FILE")
    echo "🔑 Using the development key stored in $DEV_KEY_FILE."
else
    echo "🔑 Generating a development FAASBOX_ENCRYPTION_KEY..."
    mkdir -p "$(dirname "$DEV_KEY_FILE")"
    # openssl rand -hex 32
    export FAASBOX_ENCRYPTION_KEY=4966dd3de23c98e13aa7907e00cb4c46b795ba1bd52e4602445308add9002b9d
    (umask 077 && printf '%s' "$FAASBOX_ENCRYPTION_KEY" > "$DEV_KEY_FILE")
    echo "Stored in $DEV_KEY_FILE — it is gitignored, and reused by the next run."
fi

# 3. Public address. The OAuth authorization server publishes its issuer and the
#    URL of the resource it protects, and it refuses to guess either: without
#    this variable the OAuth routes are simply not mounted. Set it to the address
#    served below so a local run has the flow working without thinking about it.
export FAASBOX_PUBLIC_URL="${FAASBOX_PUBLIC_URL:-http://127.0.0.1:8080}"
echo "🌐 Public URL: $FAASBOX_PUBLIC_URL"

# 4. Create/update superuser if credentials are provided
if [ -n "$SUPERUSER_EMAIL" ] && [ -n "$SUPERUSER_PASSWORD" ]; then
    echo "👤 Upserting superuser ($SUPERUSER_EMAIL)..."
    cd server && go run . superuser upsert "$SUPERUSER_EMAIL" "$SUPERUSER_PASSWORD" --dir=../data/pb_data && cd ..
fi

# 5. Remove previous build to detect fresh build completion
rm -f data/pb_public/index.html

# 6. Start Angular watch build in background
echo "📦 Starting Angular watch build..."
(cd ui && npx ng build --configuration=development --watch) &
NG_PID=$!
trap "kill $NG_PID 2>/dev/null" EXIT

# 7. Wait for initial build to complete
echo "⏳ Waiting for initial Angular build..."
while [ ! -f "data/pb_public/index.html" ]; do
    sleep 1
done

# 8. Start the server
echo "🏁 Starting Go server at http://127.0.0.1:8080"
echo "---"
cd server && exec go run . serve --http=127.0.0.1:8080 --dir=../data/pb_data
