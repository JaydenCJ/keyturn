#!/usr/bin/env bash
# End-to-end smoke test for keyturn: builds the binary, mints and
# verifies keys offline through the CLI, then boots the real sidecar on
# an ephemeral loopback port and drives the HTTP API. No external
# network, idempotent, finishes in seconds.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
SERVER_PID=""
cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

fail() {
  echo "SMOKE FAIL: $*" >&2
  exit 1
}

BIN="$WORKDIR/keyturn"
STORE="$WORKDIR/keys.json"

echo "1. build"
(cd "$ROOT" && go build -o "$BIN" ./cmd/keyturn) || fail "go build failed"

echo "2. version matches manifest"
[ "$("$BIN" version)" = "keyturn 0.1.0" ] || fail "version mismatch"

echo "3. mint a scoped, rate-limited key"
KEY="$("$BIN" create --store "$STORE" --name ci-bot --label live \
  --scopes read:metrics,logs:write --rate 3/1m --meta team=platform --quiet 2>/dev/null)"
case "$KEY" in
  kt_live_*) ;;
  *) fail "created key $KEY should start with kt_live_" ;;
esac
grep -q "$KEY" "$STORE" && fail "store file must never contain the full key"
grep -q '"schema_version": 1' "$STORE" || fail "store missing schema_version"

echo "4. offline verify: scopes are enforced"
OUT="$("$BIN" verify "$KEY" --store "$STORE" --scopes read:metrics)"
echo "$OUT" | grep -q "valid: ci-bot" || fail "in-scope verify should pass"
set +e
"$BIN" verify "$KEY" --store "$STORE" --scopes admin:all > "$WORKDIR/denied.txt"
[ $? -eq 1 ] || fail "out-of-scope verify should exit 1"
set -e
grep -q "denied: missing_scope" "$WORKDIR/denied.txt" || fail "missing_scope reason absent"

echo "5. offline verify: token bucket persists in the store file"
"$BIN" verify "$KEY" --store "$STORE" >/dev/null   # spends token 2 of 3
"$BIN" verify "$KEY" --store "$STORE" >/dev/null   # spends token 3 of 3
set +e
"$BIN" verify "$KEY" --store "$STORE" > "$WORKDIR/limited.txt"
[ $? -eq 1 ] || fail "fourth call should be rate-limited"
set -e
grep -q "denied: rate_limited" "$WORKDIR/limited.txt" || fail "rate_limited reason absent"

echo "6. lifecycle: revoke denies, enable restores"
LIST="$("$BIN" list --store "$STORE" --format json)"
ID="$(echo "$LIST" | grep '"id"' | head -1 | cut -d'"' -f4)"
OUT="$("$BIN" revoke "$ID" --store "$STORE")"
echo "$OUT" | grep -q "revoked $ID" || fail "revoke failed"
"$BIN" verify "$KEY" --store "$STORE" --scopes read:metrics >/dev/null 2>&1 \
  && fail "revoked key should be denied"
"$BIN" enable "$ID" --store "$STORE" >/dev/null || fail "enable failed"

echo "7. boot the sidecar on an ephemeral loopback port"
ADMIN="smoke-admin-token"
"$BIN" serve --store "$STORE" --addr 127.0.0.1:0 --admin-token "$ADMIN" \
  > "$WORKDIR/serve.log" 2>&1 &
SERVER_PID=$!
PORT=""
for _ in $(seq 1 50); do
  PORT="$(grep -o 'http://127.0.0.1:[0-9]*' "$WORKDIR/serve.log" 2>/dev/null \
    | head -1 | grep -o '[0-9]*$' || true)"
  [ -n "$PORT" ] && break
  sleep 0.1
done
[ -n "$PORT" ] || fail "server did not report its port"
BASE="http://127.0.0.1:$PORT"
HEALTH="$(curl -sf "$BASE/healthz")"
echo "$HEALTH" | grep -q '"ok": true' || fail "healthz failed"

echo "8. HTTP admin API mints a key (and rejects bad tokens)"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/v1/keys" \
  -H "Authorization: Bearer wrong" -d '{"name":"x"}')"
[ "$CODE" = "401" ] || fail "wrong admin token should get 401, got $CODE"
RESP="$(curl -sf -X POST "$BASE/v1/keys" -H "Authorization: Bearer $ADMIN" \
  -d '{"name":"acme-prod","scopes":["read:*"],"rate":"2/1m"}')"
HKEY="$(echo "$RESP" | grep -o '"key": "[^"]*"' | cut -d'"' -f4)"
case "$HKEY" in kt_*) ;; *) fail "admin create returned no key" ;; esac

echo "9. HTTP verify endpoint: allow, scope-deny, rate-limit"
OUT="$(curl -sf -X POST "$BASE/v1/verify" -d "{\"key\":\"$HKEY\",\"scopes\":[\"read:users\"]}")"
echo "$OUT" | grep -q '"valid": true' || fail "HTTP verify should pass"
OUT="$(curl -sf -X POST "$BASE/v1/verify" -d "{\"key\":\"$HKEY\",\"scopes\":[\"write:users\"]}")"
echo "$OUT" | grep -q '"reason": "missing_scope"' || fail "HTTP scope denial missing"
curl -sf -X POST "$BASE/v1/verify" -d "{\"key\":\"$HKEY\"}" >/dev/null
OUT="$(curl -sf -X POST "$BASE/v1/verify" -d "{\"key\":\"$HKEY\"}")"
echo "$OUT" | grep -q '"reason": "rate_limited"' || fail "HTTP rate limit missing"

echo "10. HTTP revoke propagates to verification"
HID="$(echo "$RESP" | grep -o '"id": "[^"]*"' | cut -d'"' -f4)"
OUT="$(curl -sf -X POST "$BASE/v1/keys/$HID/revoke" -H "Authorization: Bearer $ADMIN")"
echo "$OUT" | grep -q '"disabled": true' || fail "HTTP revoke failed"
OUT="$(curl -sf -X POST "$BASE/v1/verify" -d "{\"key\":\"$HKEY\"}")"
echo "$OUT" | grep -q '"reason": "disabled"' || fail "revoked key should verify as disabled"

echo "SMOKE OK"
