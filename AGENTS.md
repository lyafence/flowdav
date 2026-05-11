# flowdav — Agent Instructions

**Inspired by [NullLatency/FlowDriver](https://github.com/NullLatency/FlowDriver).**
Flowdav is an independent implementation; the original project does not specify a license.

## Commands

| Purpose | Command |
|---------|---------|
| Build | `make build` → binaries in `./bin/` |
| Unit tests | `make test` (race-enabled, 93 tests) |
| E2E tests | `make test-e2e` or `./scripts/test_e2e.sh` |
| E2E + encrypted configs | `make test-e2e-encrypted` |
| Full-stack Podman | `make docker-e2e` or `./scripts/prepare_test_env.sh && podman-compose up -d` |
| Test SOCKS5 | `curl --socks5h://127.0.0.1:11080 https://api.ipify.org` |
| Encrypt config | `make encrypt FILE=config.json` or `FLOWDAV_PASSWORD=secret make encrypt FILE=config.json` |
| Release archives | `make release` |
| Show version | `flowdav-client --version`, `flowdav-server --version`, `flowdav-encrypt --version` |

**Binaries:** `flowdav-client` (has `listen_addr`), `flowdav-server` (no listener), `flowdav-encrypt` (config encryption).

## Quick Reference

**Config:** `configs/flowdav_{role}.json.example` — flags: `-c` config, `-l` log level, `-p` master password, `--version`. Optional field: `max_message_size` (bytes, default 16MB).

**Health:** `GET /health` on `health_port` (e.g., `"127.0.0.1:9191"`) → JSON stats.

**WebDAV test container:**
```bash
podman run -d --name webdav-test -p 8080:8080 \
  docker.io/rclone/rclone:latest serve webdav /data \
  --addr 0.0.0.0:8080 --user test --pass test
```

## Architecture (30s version)

```
SOCKS5 ←→ client ←→ WebDAV ←→ server ←→ destination
```

- **Server has zero listening ports** for data — all via WebDAV storage.
- Client encrypts & muxes; server decrypts & demuxes. WebDAV is passive dumb storage.
- AES-256-GCM + HMAC-SHA256 on all data. Encrypted configs use PBKDF2 (600k iterations).
- Sessions: random filenames `{dir_byte}{16_hex}` — no client ID or timestamps leaked.
- DNS leak protection: raw resolver (no local DNS). UDP explicitly blocked.
- Multi-WebDAV: round-robin session assignment across backends (see `webdav.backends` in config).

## Package Map

| Package | Responsibility |
|---------|---------------|
| `cmd/flowdav-*` | Entrypoints (thin) |
| `internal/config` | Load, validate, encrypt/decrypt configs |
| `internal/transport` | Engine (poll loop, sessions), Envelope (wire format), Crypto, VirtualConn (SOCKS5), worker Pool |
| `internal/storage` | WebDAV backend + MultiBackend (circuit breaker, round-robin) |
| `internal/logger` | Leveled logging |

## Coding Conventions

- **Language:** Go 1.26.2 — use stdlib where possible.
- **Tests:** `testing` package, table-driven style, race-enabled (`-race`). Run `make test` before asking for review.
- **Imports:** stdlib first, then third-party, then internal. Grouped with blank lines.
- **Naming:** camelCase, no getters. Unexported by default.
- **Errors:** always checked and wrapped. Log via `internal/logger` package.
- **Thread safety:** explicit `sync.Mutex`, `sync.Map`, `sync.Once`. No global state. Document lock order.
- **No external HTTP or storage clients beyond WebDAV.** No raw TCP listener on server.

## Documentation Audiences

| Document | Ships in release archive | Audience | Constraints |
|----------|-------------------------|----------|-------------|
| `README.md` | ✅ Yes | End user | ALL commands must work from release tarball — binaries, example configs, README only. **Never reference `make`, `go run`, or source tree paths.** |
| `AGENTS.md` | ❌ No | Developer / Agent | Same repo. Can reference `make`, scripts (`./scripts/`), Go toolchain, source tree. |

**Rule:** When editing README.md, every command snippet must be runnable by a user who only has the release archive contents.

## Agent Constraints

- **NEVER commit or push** without explicit `commit`/`push` command.
- Unauthorized commit → user says `revert` → revert immediately, no questions.
- Destructive git operations (revert, reset, force push, rebase): **ask first**.

## Agent Workflow

1. **Read the relevant package** — understand existing patterns before writing code.
2. **Run `make test`** after any change — all tests must pass with race detector.
3. **Run `make build`** to verify compilation.
4. **If adding features to the SOCKS5/engine path**, run `make test-e2e` or `make docker-e2e`.

## Technical Debt

### 🐛 Known Gaps (fundamental, not quick fixes)
- **ACK/retransmit:** no reliability beyond Seq ordering. Envelope loss = data loss.

### 📋 Backlog (safe to fix)
- **OpenWrt cross-build** — add `GOARCH=mips`/`mipsle`/`arm` to release matrix for travel router use. ~6.8 MB client binary, static musl. Low priority.
- **Triple-encoded null byte bypass** (negligible risk)
- **Go 1.26.3** — current patch (May 2026). CI targets 1.26.3; local go.mod at 1.26.2. Still supported until Go 1.28. Plan migration to Go 1.27+ when released.
- **Metadata obfuscation** — direction subdirs (`invoices`/`receipts`), uppercase hex filenames, no extension. ✅ Added
- **Generic transport providers** — formalize `storage.Backend` contract; add S3, IMAP, filesystem relay backends. Medium priority.
- **Persistent state** — optional SQLite metadata layer for crash recovery, durable queues, resumable delivery. Low priority (not needed yet).
- **Fixed-size envelope mode** — optional padding to fixed envelope sizes. Reduces payload-size correlation analysis. Medium complexity, low priority.

### 🧪 Test Gaps (add tests here)
- `WebDAVBackend.Login()` — Mkdir error handling (not "already exists")
- `MultiBackend.isAvailable()` — cooldown expiration path
- `Envelope.Encode()` — zero coverage (unused; `EncodeWithCrypto` uses `MarshalBinary`+`Write`); `Decode()` has indirect coverage
- `Engine.gcLoop()` — tombstone TTL expiry edge cases
- `Engine.pollLoop()` — empty poll backoff reset to minPollInterval
- `pool.go` — only 4 direct tests in `pool_test.go` (improved from 2, still minimal)

### 🏗️ Architecture Weaknesses
- **Memory buffering:** entire proxied traffic in memory (txBuf per session, full file content)
