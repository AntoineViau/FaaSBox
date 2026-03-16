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

# 2. Generate encryption key (if not already set)
if [ -z "$FAASBOX_ENCRYPTION_KEY" ]; then
    echo "🔑 Generating a new FAASBOX_ENCRYPTION_KEY..."
    export FAASBOX_ENCRYPTION_KEY=$(openssl rand -hex 32)
    echo "Generated key: $FAASBOX_ENCRYPTION_KEY (save it to decrypt your secrets later)"
else
    echo "🔑 Using existing FAASBOX_ENCRYPTION_KEY."
fi

# 3. Create/update superuser if credentials are provided
if [ -n "$SUPERUSER_EMAIL" ] && [ -n "$SUPERUSER_PASSWORD" ]; then
    echo "👤 Upserting superuser ($SUPERUSER_EMAIL)..."
    cd server && go run . superuser upsert "$SUPERUSER_EMAIL" "$SUPERUSER_PASSWORD" --dir=../data/pb_data && cd ..
fi

# 4. Remove previous build to detect fresh build completion
rm -f data/pb_public/index.html

# 5. Start Angular watch build in background
echo "📦 Starting Angular watch build..."
(cd ui && npx ng build --configuration=development --watch) &
NG_PID=$!
trap "kill $NG_PID 2>/dev/null" EXIT

# 6. Wait for initial build to complete
echo "⏳ Waiting for initial Angular build..."
while [ ! -f "data/pb_public/index.html" ]; do
    sleep 1
done

# 7. Start the server
echo "🏁 Starting Go server at http://127.0.0.1:8080"
echo "---"
cd server && exec go run . serve --http=127.0.0.1:8080 --dir=../data/pb_data
