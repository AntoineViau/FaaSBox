#!/bin/bash
set -e

BASE_URL="${BASE_URL:-http://localhost:8080}"
EMAIL="${SUPERUSER_EMAIL:-test@example.com}"
PASSWORD="${SUPERUSER_PASSWORD:-testpassword1234}"

RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
NC='\033[0m'

pass=0
fail=0

check() {
  local label="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    echo -e "  ${GREEN}PASS${NC} $label"
    pass=$((pass + 1))
  else
    echo -e "  ${RED}FAIL${NC} $label (expected $expected, got $actual)"
    fail=$((fail + 1))
  fi
}

# --- Setup ---

echo -e "${CYAN}1. Get superuser token${NC}"
TOKEN=$(curl -sf -X POST "$BASE_URL/api/collections/_superusers/auth-with-password" \
  -H 'Content-Type: application/json' \
  -d "{\"identity\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" | jq -r '.token')
if [ "$TOKEN" = "null" ] || [ -z "$TOKEN" ]; then
  echo -e "  ${RED}FAIL${NC} Could not get superuser token"
  exit 1
fi
echo -e "  ${GREEN}PASS${NC} Token obtained"

echo -e "${CYAN}2. Create API key (all functions)${NC}"
KEY=$(curl -sf -X POST "$BASE_URL/api/faasbox/keys" \
  -H "Authorization: $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"test-all","allowedFunctions":["*"]}' | jq -r '.key')
echo -e "  ${GREEN}PASS${NC} Key: ${KEY:0:20}..."

echo -e "${CYAN}3. Create scoped API key (echo only)${NC}"
# A scope lists function ids, not names: a name stops designating anything the
# day the function is renamed. GET /functions is where the id comes from.
ECHO_ID=$(curl -sf "$BASE_URL/functions" -H "X-API-Key: $KEY" \
  | jq -r '.functions[] | select(.name == "echo") | .id')
if [ -z "$ECHO_ID" ] || [ "$ECHO_ID" = "null" ]; then
  echo -e "  ${RED}FAIL${NC} No function named \"echo\" to scope the key on"
  exit 1
fi
SCOPED_KEY=$(curl -sf -X POST "$BASE_URL/api/faasbox/keys" \
  -H "Authorization: $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"test-scoped\",\"allowedFunctions\":[\"$ECHO_ID\"]}" | jq -r '.key')
echo -e "  ${GREEN}PASS${NC} Key: ${SCOPED_KEY:0:20}... (scoped on $ECHO_ID)"

# --- Health check ---

echo ""
echo -e "${CYAN}4. Health check (no auth)${NC}"
HEALTH=$(curl -sf "$BASE_URL/health" | jq -r '.status')
check "returns ok" "ok" "$HEALTH"

# --- Function invocations ---

echo -e "${CYAN}5. Test echo${NC}"
ECHO_RESULT=$(curl -sf -X POST "$BASE_URL/invoke/echo" \
  -H "X-API-Key: $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"hello":"world"}')
check "result is parsed JSON" "world" "$(echo "$ECHO_RESULT" | jq -r '.result.echo.hello')"

echo -e "${CYAN}6. Test time-now${NC}"
TIME_RESULT=$(curl -sf -X POST "$BASE_URL/invoke/time-now" \
  -H "X-API-Key: $KEY" \
  -d '{}')
check "result has utc field" "true" "$(echo "$TIME_RESULT" | jq 'has("result") and (.result | has("utc"))')"

echo -e "${CYAN}7. Test ping-site${NC}"
PING_RESULT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE_URL/invoke/ping-site" \
  -H "X-API-Key: $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com"}')
# ping-site may fail on TLS in container — accept 200 or 500 (both mean the function ran)
if [ "$PING_RESULT" = "200" ] || [ "$PING_RESULT" = "500" ]; then
  check "function executed" "true" "true"
else
  check "function executed" "200 or 500" "$PING_RESULT"
fi

# --- Auth tests ---

echo ""
echo -e "${CYAN}8. Test without key (expect 401)${NC}"
STATUS=$(curl -so /dev/null -w '%{http_code}' -X POST "$BASE_URL/invoke/echo" -d '{}')
check "returns 401" "401" "$STATUS"

echo -e "${CYAN}9. Test scoped key on echo (expect 200)${NC}"
STATUS=$(curl -so /dev/null -w '%{http_code}' -X POST "$BASE_URL/invoke/echo" \
  -H "X-API-Key: $SCOPED_KEY" -d '{}')
check "returns 200" "200" "$STATUS"

echo -e "${CYAN}10. Test scoped key on time-now (expect 403)${NC}"
STATUS=$(curl -so /dev/null -w '%{http_code}' -X POST "$BASE_URL/invoke/time-now" \
  -H "X-API-Key: $SCOPED_KEY" -d '{}')
check "returns 403" "403" "$STATUS"

echo -e "${CYAN}10b. Invoke echo by its id (expect 200)${NC}"
STATUS=$(curl -so /dev/null -w '%{http_code}' -X POST "$BASE_URL/invoke/$ECHO_ID" \
  -H "X-API-Key: $SCOPED_KEY" -d '{}')
check "the id reaches the same function" "200" "$STATUS"

# --- Dependencies (ticket 04) ---

echo ""
echo -e "${CYAN}11. Test function with npm deps (test-deps)${NC}"
DEPS_RESULT=$(curl -sf -X POST "$BASE_URL/invoke/test-deps" \
  -H "X-API-Key: $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"duration":"2h"}')
check "ms package works" "7200000" "$(echo "$DEPS_RESULT" | jq -r '.result.ms')"
check "input echoed" "2h" "$(echo "$DEPS_RESULT" | jq -r '.result.input')"

# --- Stdout/stderr separation (ticket 05) ---

echo ""
echo -e "${CYAN}12. Test stdout/stderr separation${NC}"
STDERR_RESULT=$(curl -sf -X POST "$BASE_URL/invoke/test-stderr" \
  -H "X-API-Key: $KEY" -d '{}')
check "result from stdout only" "ok" "$(echo "$STDERR_RESULT" | jq -r '.result.status')"
check "stderr captured separately" "true" "$(echo "$STDERR_RESULT" | jq 'has("stderr")')"
check "stderr contains debug log" "true" "$(echo "$STDERR_RESULT" | jq '.stderr | contains("debug:")')"

echo -e "${CYAN}13. Test list functions${NC}"
LISTING=$(curl -sf "$BASE_URL/functions" -H "X-API-Key: $KEY")
check "5 functions found" "5" "$(echo "$LISTING" | jq -r '.count')"
check "every entry carries an id" "true" \
  "$(echo "$LISTING" | jq '[.functions[] | has("id") and (.id | length > 0)] | all')"

# --- Summary ---

echo ""
echo -e "---"
echo -e "${GREEN}$pass passed${NC}, ${RED}$fail failed${NC}"
[ "$fail" -eq 0 ] && exit 0 || exit 1
