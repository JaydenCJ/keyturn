# The verification API

keyturn's whole contract is one endpoint plus a small admin surface.
This document is the wire-level reference; the README covers usage.

## Key format

```
kt_live_8b0kdmvyq2_c3vjkm5rw8xz0q4t7ye2ahsgn6pd
└┬┘ └┬─┘ └───┬────┘ └────────────┬─────────────┘
 │   │       │                   └ secret (28 chars, random)
 │   │       └ key ID (10 chars, random, safe to log)
 │   └ optional label (1-16 lowercase alphanumerics)
 └ fixed product prefix
```

- Random segments use lowercase Crockford base32 (`0-9` and `a-z`
  without `i l o u`), so keys survive copy-paste and are unambiguous
  to human eyes. Labels are human-chosen and allow all of `a-z0-9`.
- keyturn stores **only** `SHA-256(full key string)` (hex) plus the ID.
  A leaked store file yields no usable credentials.
- The ID is public: log it, put it in dashboards, reference it in
  `revoke`/`delete`. The secret appears exactly once, at creation.

## `POST /v1/verify` (public)

Request body:

```json
{ "key": "kt_…", "scopes": ["read:users"], "cost": 1 }
```

| Field | Required | Meaning |
|---|---|---|
| `key` | yes | the full presented key string |
| `scopes` | no | scopes this call demands; empty = any valid key |
| `cost` | no | tokens to spend (default 1; below 1 counts as 1) |

Every *definitive* answer is HTTP 200 with a JSON body; your proxy
should treat non-200 as "the sidecar is broken", never as "key denied".
400 = malformed JSON / missing `key` field; 413 = body over 64 KiB.

Response fields:

| Field | Present | Meaning |
|---|---|---|
| `valid` | always | the one bit that matters |
| `reason` | on denial | stable code, see the table below |
| `key_id`, `name`, `label`, `scopes`, `meta` | once the secret matched | who this key is |
| `missing_scopes` | `missing_scope` only | exactly which demanded scopes the key lacks |
| `remaining` | always | whole tokens left; `-1` = unlimited or not evaluated |
| `retry_after_ms` | `rate_limited` only | wait hint; `-1` = never (cost exceeds burst) |

## Denial reasons

The ladder runs in a fixed order; the first failing rung answers.

| Reason | Rung | Notes |
|---|---|---|
| `malformed` | parse | not shaped like a keyturn key |
| `not_found` | lookup + hash | unknown ID **and** wrong secret give this same answer, deliberately — distinguishing them would confirm which IDs exist |
| `disabled` | revocation | flipped by `revoke` / `enable` |
| `expired` | expiry | exclusive boundary: dead *at* the expiry instant |
| `missing_scope` | authorization | never spends tokens |
| `rate_limited` | token bucket | the only rung that mutates state |

## Admin surface (`Authorization: Bearer <admin-token>`)

Enabled only when `keyturn serve` is started with `--admin-token` (or
`KEYTURN_ADMIN_TOKEN`); otherwise every admin route answers 403.

| Route | Effect |
|---|---|
| `POST /v1/keys` | mint a key; the response is the only time the full key exists |
| `GET /v1/keys` | list records (never hashes or secrets) |
| `GET /v1/keys/{id}` | one record |
| `POST /v1/keys/{id}/revoke` | disable without deleting |
| `POST /v1/keys/{id}/enable` | re-enable |
| `DELETE /v1/keys/{id}` | remove permanently |
| `GET /healthz` | liveness + key count (no auth) |

## Store file

A single JSON document (`schema_version: 1`), written atomically
(temp file + rename) with mode 0600. Records carry the hash, scopes,
limit, expiry, metadata, and the token-bucket state. CLI verification
persists spent tokens back to the file so offline gating works across
invocations; the server keeps bucket state in memory and a restart
refills buckets (documented trade-off, see README roadmap).
