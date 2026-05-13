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
- Random filenames `{dir_byte}{16_hex}` — no metadata leaks.
- DNS leak protection: raw resolver, UDP explicitly blocked.
- Multi-WebDAV: random session assignment, round-robin upload fallback.
- No global state.
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
- **ACK/retransmission** — control packets add predictable patterns. Best-effort is deliberate.
- **Persistent state / SQLite** — local artifacts and disk writes, no need.
- **Verbose logging / event tracing** — if ever needed: local-only, off by default.
- **Remote metrics (Prometheus, etc.)** — if ever needed: local-only, off by default.
- **Config flag creep** — not every internal constant needs a user-facing flag. Defaults are meant to be defaults.

## Known Weaknesses

- **Memory buffering:** all proxied traffic kept in memory (txBuf, full file content).
- **ACK/retransmit:** no reliability beyond Seq ordering. Envelope loss = data loss.
## Test Gaps

- `MultiBackend.isAvailable()` — cooldown expiry (`internal/storage/multi.go:43`)
- `Engine.gcLoop()` — tombstone TTL edge cases (`internal/transport/engine.go:453`)
- `Engine.pollLoop()` — empty poll backoff reset (`internal/transport/engine.go:305`)
> Remove entries from this list once fixed.

## Backlog

| Tag | Priority | Item | Notes |
|-----|----------|------|-------|
| 📋 | P1 | **Protocol version field** in envelope | `internal/transport/envelope.go:40-87`, ~10 lines, zero wire overhead (1 extra byte) |
| 📋 | P2 | **Protocol documentation** (`protocol.md`) | No runtime impact |
| ⚠️ | P2 | **Local-only metrics** — extend `Stats()` with queue depth, retry counters. Serve on `health_port` ONLY (localhost). | medium effort. **Never** add remote scrape endpoints. |
| 📋 | ++ | **Fixed-size envelope mode** — optional padding | Medium priority. Reduces payload-size correlation. |
| 📋 | ++ | **OpenWrt cross-build** | Low priority. No profile impact. |
| ⚠️ | ++ | **Generic transport providers** (S3, IMAP) | Medium priority. Each provider has distinct traffic patterns — verify against profile goals. |
| ⚠️ | ++ | **Buffer pooling** — `sync.Pool` for txBuf | Low priority. No network impact. |
| ⚠️ | ++ | **Fuzz testing** | Low priority. Dev-only, no runtime impact. |
