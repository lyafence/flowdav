# flowdav — Agent Instructions

## Commands

| Command | What |
|---------|------|
| `make build` | Build unified binary → `./bin/flowdav` |
| `make check` | Full verification: vet → lint → build → test |
| `make test` | Unit tests with race detector |
| `make test-short` | Unit tests without race detector |
| `make test-e2e` | E2E tests (requires podman/docker) |
| `make test-e2e-encrypted` | E2E + encrypted configs |
| `make docker-e2e` | Build + E2E via podman |
| `make encrypt FILE=config.json` | Encrypt config (also: `FLOWDAV_PASSWORD=secret make encrypt`) |
| `make fuzz` | Run fuzz tests (30s each: envelope, crypto, config) |
| `make release` | Release archives |
| `make openwrt` | Cross-compile for MIPS little-endian, softfloat |
| `make hooks` | Install pre-commit hooks (recommended) |
| `make android-apk` | Build Android APK (debug) |
| `make android-deploy` | Build APK + start test env + deploy to Android device |
| `flowdav --version` | Show version |
| `make compose-android` | Build Docker images for Android test env |
| `curl --socks5h://127.0.0.1:11080 https://api.ipify.org` | Test SOCKS5 via docker-compose |

## Package Map

| Package | Responsibility |
|---------|---------------|
| `cmd/flowdav` | Entrypoints (thin) — unified binary |
| `cmd/android` | Gomobile bridge (exported to Android) |
| `internal/config` | Load, validate, encrypt/decrypt configs |
| `internal/transport` | Engine (poll loop, sessions), Envelope, Crypto, VirtualConn (SOCKS5), Pool |
| `internal/storage` | WebDAV backend + MultiBackend (circuit breaker, round-robin) |
| `internal/logger` | Leveled logging |

## Architecture

```
SOCKS5 ←→ client ←→ WebDAV ←→ server ←→ destination
```

**Binary:** `flowdav` — unified entrypoint with `-c` (client), `-s` (server), `-e` (encrypt), `-g` (generate config) modes.
**Android bridge:** `cmd/android/bridge.go` — gomobile bind, exports `StartProxyFromData`/`StartProxyManual`/`StopProxy`/`GetStatus`/`StopAndError`/`SetSocks5Auth` to Kotlin.

## Design Invariants

- Server has **zero listening ports** for data — all data via WebDAV storage (optional health endpoint on loopback only).
- WebDAV is passive dumb storage; client encrypts/muxes, server decrypts/demuxes.
- AES-256-GCM + HMAC-SHA256 on all data.
- Random filenames `{dir_byte}{16_hex}` — no metadata leaks. Mapped to `{subdir}/{uppercase_hex}` on storage (direction byte → subdirectory: `r`→`invoices`, `s`→`receipts`).
- DNS leak protection: raw resolver, UDP explicitly blocked.
- Multi-WebDAV: random session assignment, round-robin upload fallback.
- No global state (exception: `transport.MaxMessageSize` / `storage.MaxFileSize` — package-level vars set at startup, justified by OOM prevention per `internal/transport/crypto.go:120`).
- **Operational philosophy:** minimize external observability, avoid predictable patterns. When adding anything network-facing: is it optional? bounded? indistinguishable from noise?

## Pre-commit Hook

The pre-commit hook (`.githooks/pre-commit`) checks:
- `gofmt` on `cmd/ internal/`
- `goimports` with `-local github.com/lyafence/flowdav`
- `go vet ./...`
- Bans `math/rand`, `os/exec` in production code
- Bans `database/sql` without justification
- Bans `sync.Pool` without benchmark

Install with `make hooks`.

## Config Quick Reference

- Flags: `-c config.json` (client), `-s config.json` (server), `-e config.json` (encrypt), `-g config.json` (generate), `-p master_password`, `-l loglevel`, `--version`
- Fields: `enc_key` / `hmac_key` (32-byte base64), `max_message_size` (default 16MB), `max_sessions` (default 0 = unlimited), `webdav.backends` (array), `health_port` (e.g. `"127.0.0.1:9191"`), `log_level` (`debug`, `info`, `warn`, `error`)
- Health: `GET /health` on `health_port` → JSON stats (active sessions, retry counters, tx queue depth, per-backend circuit breaker state)

## Documentation Audiences

| Document | Ships in archive | Audience | Constraint |
|----------|-----------------|----------|------------|
| `README.md` | Yes | End user | All commands must work from release tarball. **Never reference `make`, `go run`, or source tree paths.** |
| `AGENTS.md` | No | Agent | Can reference `make`, scripts, Go toolchain. |
| `CONTRIBUTING.md` | No | Developer / Agent | Development workflow, code style, PR guide. AGENTS.md is authoritative for agents. |

## Coding Conventions

- Go 1.26.3, stdlib preferred.
- Tests: `testing` package, table-driven, `-race` enabled.
- Imports: stdlib → third-party → internal, blank-line separated.
- Naming: camelCase, no getters, unexported by default.
- Errors: always checked and wrapped. Log via `internal/logger`.
- Thread safety: explicit `sync.Mutex`/`Map`/`Once`. Document lock order.

## Agent Constraints

- **NEVER commit or push** without explicit command.
- Unauthorized commit → user says `revert` → revert immediately.
- Destructive git operations (revert, reset, force push, rebase): **ask first**.

## Agent Workflow

1. Read the relevant package before writing code.
2. `make check` after any change — full verification (vet → lint → build → test with race detector).
3. SOCKS5/engine changes → `make test-e2e` or `make docker-e2e` (requires podman).
4. Encrypted config changes → `make test-e2e-encrypted`.
5. After adding/removing dependencies → `go mod tidy` (CI checks `git diff go.mod go.sum`).

## Anti-Patterns

- **Coverage Theater** — don't write tests you can't name a real bug for.
- **Refactoring for Testability** — test through public API, not extracted private methods.
- **Don't clean what you don't understand** — double-select shutdown, redundant Store guards — often intentional.
- **No cargo cult** — understand why a pattern exists before copying it. Just because it's in the codebase doesn't mean it belongs in your change.
- **YAGNI** — don't add features "just in case". If you don't need it today, you probably won't need it tomorrow. Adding it later is cheaper than maintaining it now.
- **Memory buffering** — txBuf per session is deliberate (2MB cap, blocks on backpressure instead of extra HTTP calls). `sync.Pool` for txBuf is premature without profiling.
- **ACK/retransmission** — control packets add predictable patterns. Best-effort is deliberate.
- **Persistent state / SQLite** — local artifacts and disk writes, no need.
- **Verbose logging / event tracing** — if ever needed: local-only, off by default.
- **Remote metrics (Prometheus, etc.)** — if ever needed: local-only, off by default.
- **Config flag creep** — not every internal constant needs a user-facing flag. Defaults are meant to be defaults.

## Backlog

| Priority | Item | Effort |
|----------|------|--------|
| P4 | **Fixed-size envelope mode** — optional padding against payload-size correlation. *Under question — TLS-level analysis bypasses padding anyway* | high |
