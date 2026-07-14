# keyturn examples

Two runnable examples, both fully offline.

## `demo.sh` — CLI walkthrough

Mints a scoped, rate-limited key in a temp store, then walks it through
every verification outcome: in-scope, out-of-scope, rate-limited,
revoked.

```bash
bash examples/demo.sh
```

## `middleware/` — protecting a Go service with the sidecar

A minimal upstream service (`middleware/main.go`) that answers
`GET /reports` only for callers whose `Authorization: Bearer kt_…` key
the keyturn sidecar accepts with the `read:reports` scope. This is the
integration pattern: your service (or proxy) makes one POST per request
and forwards or rejects based on `valid`.

```bash
# terminal 1 — the sidecar
keyturn create --name demo --scopes read:reports --rate 10/1m --store /tmp/demo.json
keyturn serve --store /tmp/demo.json

# terminal 2 — the protected service
go run ./examples/middleware

# terminal 3 — a caller
curl -H "Authorization: Bearer kt_…" http://127.0.0.1:8080/reports
```

The middleware treats any sidecar transport failure as 503 (fail
closed), maps `rate_limited` to 429 with a `Retry-After` header, and
every other denial to 403.
