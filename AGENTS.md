# flowdav — Agent Instructions

**Inspired by [NullLatency/FlowDriver](https://github.com/NullLatency/FlowDriver).**
Flowdav is an independent implementation; the original project does not specify a license.

## Commands

| Purpose | Command |
|---------|---------|
| Build | `make build` → binaries in `./bin/` |
| Unit tests | `make test` (race-enabled, 90+ tests) |
| E2E tests | `make test-e2e` or `./scripts/test_e2e.sh` |
| E2E + encrypted configs | `make test-e2e-encrypted` |
| Full-stack Podman | `make docker-e2e` or `./scripts/prepare_test_env.sh && podman-compose up -d` |
| Test SOCKS5 | `curl --socks5h://127.0.0.1:11080 https://api.ipify.org` |
| Encrypt config | `make encrypt FILE=config.json` or `FLOWDAV_PASSWORD=secret make encrypt FILE=config.json` |
| Release archives | `make release` |

**Binaries:** `flowdav-client` (has `listen_addr`), `flowdav-server` (no listener), `flowdav-encrypt` (config encryption).

## Quick Reference

**Config:** `configs/flowdav_{role}.json.example` — flags: `-c` config, `-l` log level, `-p` master password, `--version`.

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
- Sessions: random filenames `{dir_byte}{16_hex}.bin` — no client ID or timestamps leaked.
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

## Agent Workflow

1. **Read the relevant package** — understand existing patterns before writing code.
2. **Run `make test`** after any change — all tests must pass with race detector.
3. **Run `make build`** to verify compilation.
4. **If adding features to the SOCKS5/engine path**, run `make test-e2e` or `make docker-e2e`.

## Technical Debt

### ✅ Refactored (leave alone)
- Worker pool in `engine.pollLoop`
- Deep copy of `Envelope.Payload` in `session.rxQueue`
- `sync.Once` for `VirtualConn.Close()`
- Circuit breaker in `MultiBackend`
- Hand-rolled PBKDF2 → `crypto/pbkdf2` (stdlib)
- Configurable adaptive polling (min/max bounds, exponential backoff)
- Random filenames without client ID/timestamps
- `s.BackendIdx` read under `s.mu` in flushAll
- `time.After` → reusable timer in `ProcessRx`
- Overflow guard in `MarshalBinary`
- WebDAV Delete error checked before removing processed entry

### 🐛 Known Gaps (fundamental, not quick fixes)
- **ACK/retransmit:** no reliability beyond Seq ordering. Envelope loss = data loss.

### 📋 Backlog (safe to fix)
- **OpenWrt cross-build** — add `GOARCH=mips`/`mipsle`/`arm` to release matrix for travel router use. ~6.8 MB client binary, static musl. Low priority.
- **Polling jitter** — add random jitter (±25%) around poll intervals to reduce traffic fingerprinting. ~15 lines.
- **Envelope compression** — gzip/deflate payload before encryption. ~3-5x compression on HTML/JSON/text. High ROI for bandwidth-limited WebDAV.
- **1ms busy-wait** on RxChan graceful close — `conn.go:80`
- **`DownloadWorkerPool.Submit` can stall `pollLoop`** — ~~`pool.go:65-70` (channel full = poll loop blocks)~~ ✅ Non-blocking with auto-retry
- **`inFlight` sync.Map entries persist** on shutdown (dead code, zero impact)
- **Double semaphore** — ~~`downloadSem` (cap 16) redundant under `sem` (cap 8) — `pool.go:81-85`~~ ✅ Removed
- **Missing security linters** in `.golangci.yml` ~~(`gosec`, `bodyclose`, `noctx`)~~ ✅ Added
- **Triple-encoded null byte bypass** (negligible risk)
- **`TestFilenameParsingWithDashedClientID`** — ~~tests old filename format, dead code~~
- **Go 1.26.3** — current patch (May 2026). CI targets 1.26.3; local go.mod at 1.26.2. Still supported until Go 1.28. Plan migration to Go 1.27+ when released.
- **Retry for storage ops** — upload/download/delete with 3 attempts, exponential backoff (100ms, 200ms). ✅ Added
- **Metadata obfuscation** — directory bucketing (`ab/cd/` from first 2 filename bytes) to reduce per-directory scan overhead and hide traffic patterns. Low/medium priority.
- **Generic transport providers** — formalize `storage.Backend` contract; add S3, IMAP, filesystem relay backends. Medium priority.
- **MaxEnvelopeSize in config** — make `MaxMessageSize` (hardcoded 16MB) configurable per-deployment. Low priority.
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
- **No retry** for failed storage operations (upload, download, delete) — ✅ Added
- **Memory buffering:** entire proxied traffic in memory (txBuf per session, full file content)
