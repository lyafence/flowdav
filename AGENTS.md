# flowdav — Agent Instructions

## Commands

| Command | What |
|---------|------|
| `make build` | Build all binaries → `./bin/` |
| `make test` | Unit tests with race detector |
| `make test-e2e` | E2E tests |
| `make test-e2e-encrypted` | E2E + encrypted configs |
| `make docker-e2e` | Full-stack Podman |
| `make encrypt FILE=config.json` | Encrypt config (also: `FLOWDAV_PASSWORD=secret make encrypt FILE=config.json`) |
| `make release` | Release archives |
| `make openwrt` | Cross-compile for OpenWrt (MIPS little-endian, softfloat) |
| `make fuzz` | Run fuzz tests (envelope parser, crypto, config loader) |
| `flowdav-* --version` | Show version (client, server, encrypt) |
| `curl --socks5h://127.0.0.1:11080 https://api.ipify.org` | Test SOCKS5 proxy |

## Package Map

| Package | Responsibility |
|---------|---------------|
| `cmd/flowdav-*` | Entrypoints (thin) |
| `internal/config` | Load, validate, encrypt/decrypt configs |
| `internal/transport` | Engine (poll loop, sessions), Envelope, Crypto, VirtualConn (SOCKS5), Pool |
| `internal/storage` | WebDAV backend + MultiBackend (circuit breaker, round-robin) |
| `internal/logger` | Leveled logging |

## Architecture

```
SOCKS5 ←→ client ←→ WebDAV ←→ server ←→ destination
```

**Binaries:** `flowdav-client` (has `listen_addr`), `flowdav-server` (no listener), `flowdav-encrypt` (config encryption).

## Design Invariants

- Server has **zero listening ports** for data — all data via WebDAV storage (optional health endpoint on loopback only).
- WebDAV is passive dumb storage; client encrypts/muxes, server decrypts/demuxes.
- AES-256-GCM + HMAC-SHA256 on all data.
- Random filenames `{dir_byte}{16_hex}` — no metadata leaks. Mapped to `{subdir}/{uppercase_hex}` on storage (direction byte → subdirectory: `r`→`invoices`, `s`→`receipts`).
- DNS leak protection: raw resolver, UDP explicitly blocked.
- Multi-WebDAV: random session assignment, round-robin upload fallback.
- No global state (exception: `transport.MaxMessageSize` / `storage.MaxFileSize` — package-level vars set at startup, justified by OOM prevention per `crypto.go:120`).
- **Operational philosophy:** minimize external observability, avoid predictable patterns. When adding anything network-facing: is it optional? bounded? indistinguishable from noise?

## Config Quick Reference

- Flags: `-c config.json`, `-l loglevel`, `-p master_password`, `--version`
- Fields: `enc_key` / `hmac_key` (32-byte base64), `max_message_size` (default 16MB), `max_sessions` (default 0 = unlimited), `webdav.backends` (array), `health_port` (e.g. `"127.0.0.1:9191"`)
- Health: `GET /health` on `health_port` → JSON stats

## Documentation Audiences

| Document | Ships in archive | Audience | Constraint |
|----------|-----------------|----------|------------|
| `README.md` | 📋 Yes | End user | All commands must work from release tarball. **Never reference `make`, `go run`, or source tree paths.** |
| `AGENTS.md` | ❌ No | Agent | Can reference `make`, scripts, Go toolchain. |
| `CONTRIBUTING.md` | ❌ No | Developer / Agent | Development workflow, code style, PR guide. Overlaps with AGENTS.md by design — AGENTS.md is authoritative for agents. |

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
2. `make test` after any change — must pass with race detector.
3. `make build` to verify compilation.
4. SOCKS5/engine changes → `make test-e2e` or `make docker-e2e`.
5. Encrypted config changes → `make test-e2e-encrypted`.

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
| P2 | **Protocol documentation** (`protocol.md`) — binary format, crypto, direction byte | low |
| P2 | **Local-only metrics** — queue depth, retry counters on `health_port`. No remote scrape endpoints | medium |
| P3 | **Fixed-size envelope mode** — optional padding against payload-size correlation | high |
