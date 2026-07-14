# Contributing to keyturn

Issues, discussions and pull requests are all welcome.

## Getting started

You need Go ≥1.22 and (for the smoke test) `curl`; nothing else.

```bash
git clone https://github.com/JaydenCJ/keyturn && cd keyturn
go build ./...
go test ./...
bash scripts/smoke.sh
```

`scripts/smoke.sh` builds the binary, mints and verifies keys offline
through the CLI, then boots the real sidecar on an ephemeral loopback
port and drives the HTTP API end to end; it must finish by printing
`SMOKE OK`.

## Before you open a pull request

1. `gofmt -l .` reports nothing (formatting is enforced).
2. `go vet ./...` passes with no findings.
3. `go test ./...` passes (89 deterministic tests, no network, no sleeps).
4. `bash scripts/smoke.sh` prints `SMOKE OK`.
5. Add tests for behavior changes; keep logic in pure, unit-testable
   modules — the token bucket, scope matcher, and verification ladder
   never touch a clock or a socket directly, and that must stay true.

## Ground rules

- Keep dependencies at zero: keyturn is standard library only, and
  adding one needs strong justification in the PR.
- No network calls at startup, no telemetry, ever. The sidecar binds
  loopback by default and only speaks when spoken to.
- Denial reasons (`not_found`, `rate_limited`, …) and exit codes are a
  public contract — changing them is a breaking change.
- Wrong-secret and unknown-ID must stay indistinguishable to callers;
  never "improve" an error message in a way that separates them.
- Code comments and doc comments are written in English.
- Determinism first: tests inject the clock and randomness; a test that
  sleeps or reads the wall clock will be rejected.

## Reporting bugs

Include the output of `keyturn version`, the exact command or HTTP
request, the response you got (redact secrets — the `kt_…_` prefix and
ID are enough), and, for verification bugs, the record as shown by
`keyturn show ID --format json`, which is exactly what the ladder sees.

## Security

Please do not open public issues for security problems; use GitHub's
private vulnerability reporting on this repository instead.
