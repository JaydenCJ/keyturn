#!/usr/bin/env bash
# CLI walkthrough: mint a key, then hit every verification outcome.
# Fully offline; uses a temp store and cleans up after itself.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

BIN="$WORKDIR/keyturn"
STORE="$WORKDIR/keys.json"
(cd "$ROOT" && go build -o "$BIN" ./cmd/keyturn)

echo "# mint a key: 3 requests/minute, read-only scopes"
KEY="$("$BIN" create --store "$STORE" --name demo-bot \
  --scopes 'read:*' --rate 3/1m --quiet 2>/dev/null)"
echo "  $KEY"
echo

echo "# in-scope verify (spends 1 token)"
"$BIN" verify "$KEY" --store "$STORE" --scopes read:users
echo

echo "# out-of-scope verify (denied, spends nothing)"
"$BIN" verify "$KEY" --store "$STORE" --scopes write:users || true
echo

echo "# drain the bucket, then watch the limiter kick in"
"$BIN" verify "$KEY" --store "$STORE" >/dev/null
"$BIN" verify "$KEY" --store "$STORE" >/dev/null
"$BIN" verify "$KEY" --store "$STORE" || true
echo

echo "# revoke, and the same key is dead"
ID="$("$BIN" list --store "$STORE" --format json | grep '"id"' | head -1 | cut -d'"' -f4)"
"$BIN" revoke "$ID" --store "$STORE"
"$BIN" verify "$KEY" --store "$STORE" || true
