# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-07-13

### Added

- Key format `kt_[label_]id_secret`: lowercase Crockford-base32 random
  segments, optional human-chosen label, 10-char public ID, 28-char
  secret; only the SHA-256 hash is ever stored, compared in constant
  time, with a `Redact` helper for safe logging.
- Verification ladder with stable denial reasons — `malformed`,
  `not_found`, `disabled`, `expired`, `missing_scope`, `rate_limited` —
  where unknown ID and wrong secret deliberately return the same
  answer, and scope failures never spend rate-limit tokens.
- Scopes with trailing-wildcard grants (`read:*`, `*`), validation,
  duplicate rejection, and exact missing-scope reporting.
- Per-key token-bucket rate limiting: `N/window` syntax (`100/1m`,
  `10/s`), separate burst capacity, continuous refill, exact
  `retry_after` hints, monotonicity against clock jumps, and a pure
  `Take` function driven entirely by an injected clock.
- Single-file JSON store (`schema_version: 1`): atomic temp-file +
  rename writes, 0600 permissions, byte-stable output, schema and
  duplicate-ID guards at load, deep-copy reads.
- HTTP sidecar (`keyturn serve`, loopback by default): public
  `POST /v1/verify` answering every definitive result with 200, plus a
  bearer-token admin surface (`/v1/keys` create/list/get/revoke/
  enable/delete) that is disabled entirely when no token is configured.
- CLI: `create` (with `--label`, `--scopes`, `--rate`, `--burst`,
  `--expires`, `--meta`, `--quiet`), `list`, `show`, `revoke`,
  `enable`, `delete`, `verify` (offline, persists spent tokens to the
  store file), `serve`, `version`; text and JSON output; scriptable
  exit codes (0 valid, 1 denied, 2 usage, 3 runtime).
- Runnable examples (`examples/demo.sh`, `examples/middleware/`) and a
  wire-level API reference (`docs/verification-api.md`).
- 89 deterministic offline tests (unit + in-process HTTP and CLI
  integration, injected clocks and randomness) and `scripts/smoke.sh`.

[0.1.0]: https://github.com/JaydenCJ/keyturn/releases/tag/v0.1.0
