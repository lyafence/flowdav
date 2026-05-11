# flowdav — Agent Instructions

**Inspired by [NullLatency/FlowDriver](https://github.com/NullLatency/FlowDriver) — credit to NullLatency for the original concept.**
Flowdav is an independent implementation; the original project does not specify a license.

## Build
```bash
# Quick build (binaries to ./bin)
make build

# Build in Podman and extract binaries
make docker-build
# or: make docker-e2e  (build + test)

# Release archives (multi-platform .tar.gz)
make release
```

## Run
- Configs: `configs/flowdav_{role}.json.example` (client has `listen_addr`, server does not)
- Flags: `-c` config, `-l` log level (debug|info|warn|error), `-p` master password (for .enc configs), `--version`
- Health endpoint: `GET /health` returns JSON stats (active sessions, processed files, role, rates)
- Build: `make build` or `make image-to-bin`

## Architecture
- Server has **no listening ports** for data — all communication via WebDAV storage
- Optional HTTP health endpoint: set `health_port` in config (e.g., `"127.0.0.1:9191"`) to enable `GET /health` returning JSON engine stats
- Data flow: `[SOCKS5] ←→ client ←→ WebDAV ←→ server ←→ destination`
  (client encrypts & muxes; server decrypts & demuxes; WebDAV is passive storage)
- Sessions use random filenames `{dir_byte}{16_hex}.bin` (direction byte + random hex, no client ID or timestamp leakage)
- Encryption: AES-256-GCM + HMAC-SHA256 (via `enc_key`/`hmac_key`) + PBKDF2 key derivation for encrypted configs
- DNS leak protection: client uses raw resolver (no local DNS lookups)
- UDP explicitly blocked

## Storage Backends
- **WebDAV only**: `storage_type: "webdav"` with `provider: "custom"`, `url`, `login`, `token`.
- **Multi-WebDAV**: Support for multiple WebDAV providers with round-robin session assignment. Configure via `webdav.backends` array in the config.
  - Example:
    ```json
    "webdav": {
      "backends": [
        {"provider": "custom", "url": "http://webdav1:8080", "login": "user1", "token": "pass1", "base_path": "app1"},
        {"provider": "custom", "url": "http://webdav2:8080", "login": "user2", "token": "pass2", "base_path": "app2"}
      ]
    }
    ```
- Test with: `podman run -d --name webdav-test -p 8080:8080 docker.io/rclone/rclone:latest serve webdav /data --addr 0.0.0.0:8080 --user test --pass test`

## Testing
- **Unit tests**: `make test` (100+ tests across 7 packages, race-enabled)
- **E2E tests**: `make test-e2e` or `./scripts/test_e2e.sh`
- **Encrypted config E2E**: `make test-e2e-encrypted` or `./scripts/test_e2e.sh --encrypted`
- **Full-stack with Podman**:
  1. `make docker-e2e` — builds image, runs compose, tests SOCKS5 proxy
  2. Or manually: `podman-compose -f docker-compose.yml up -d`
  3. Services: 3× webdav (single + multi), flow-server, flow-client, flow-server-multi, flow-client-multi
  4. Test: `curl --socks5h://127.0.0.1:11080 https://api.ipify.org`

## Encrypted Configs
- Create a config JSON (start from `.json.example`), then encrypt:
  ```bash
  make encrypt FILE=config.json
  # or with env var (non-interactive):
  FLOWDAV_PASSWORD=secret make encrypt FILE=config.json
  ```
- Or use the standalone binary directly:
  ```bash
  FLOWDAV_PASSWORD=secret ./flowdav-encrypt --gen-keys < config.json > config.enc
  ```
- `--gen-keys` auto-generates `enc_key` and `hmac_key` if missing
- Test env: `./scripts/prepare_test_env.sh --encrypted`
- Master password via `-p <password>` flag or `FLOWDAV_PASSWORD` env var

## Notes
- Go 1.26.2 (see `go.mod`)
- Client handles SIGINT/SIGTERM: closes listener and cancels context for graceful shutdown
- SOCKS5 authentication: set `socks5_user` and `socks5_pass` in config for production
- `stop_grace_period: 10s` in docker-compose matches the signal handling

## Security
- Encryption: AES-256-GCM + HMAC-SHA256 (mandatory)
- Encrypted configs: PBKDF2-HMAC-SHA256 (600k iterations) + AES-256-GCM
- SOCKS5 auth: username/password (recommended for production)
- All traffic encrypted in WebDAV storage
- Server has no exposed ports — reduced attack surface

## Technical Debt
- **Tests** (current): 100+ tests across 7 packages, race-enabled
  - `internal/config`: 33+ tests (Load, validation, crypto, encrypted config, ResolvePassword)
  - `internal/storage`: 10+ tests (fullPath, isLocalURL, validateNotPrivateURL, multi-backend)
  - `internal/transport`: 37+ tests (Session, Engine, Envelope, Crypto, VirtualConn, worker pool, race tests)
  - `internal/logger`: 4 tests (SetLevel, logging levels, filtering)
  - `internal/config/e2e_test.go`: E2E test for encrypted config flow (builds binaries, tests -p flag, FLOWDAV_PASSWORD, wrong password)
- **Refactoring completed**:
  - Worker pool in `engine.pollLoop` ✅
  - Deep copy of Envelope.Payload in session.rxQueue ✅
  - `sync.Once` for VirtualConn.Close() ✅
  - Circuit breaker in MultiBackend ✅
  - Hand-rolled PBKDF2 replaced with `crypto/pbkdf2` (stdlib) ✅
  - Configurable adaptive polling (min/max bounds, exponential backoff) ✅
  - Metadata obfuscation: random filenames without client ID or timestamps ✅
  - Data race on `s.BackendIdx` — read under `s.mu` in flushAll ✅
  - `time.After` timer leak — replaced with reusable timer in ProcessRx ✅
  - Buffer overflow latent risk — overflow guard added in MarshalBinary ✅
  - WebDAV Delete error — now checked before removing processed entry ✅
- **Known gaps**:
  - ACK/retransmit layer: no reliability mechanism beyond Seq ordering (envelope loss = data loss)
- **Backlog**:
  - Goroutine spin (1ms busy-wait) on RxChan graceful close — `conn.go:80`
  - `DownloadWorkerPool.Submit` can stall `pollLoop` under load when all workers busy — `pool.go:65-70`
  - `inFlight` sync.Map entries persist on shutdown (dead code, zero functional impact)
  - Double semaphore — `downloadSem` (cap 16) redundant under `sem` (cap 8) — `pool.go:81-85`
  - Missing security linters in `.golangci.yml` (`gosec`, `bodyclose`, `noctx`)
  - Triple-encoded null byte bypass (documented limitation, negligible practical risk)
  - Go 1.26 EOL Feb 2026 — plan migration to Go 1.27+
- **Test gaps**:
  - `WebDAVBackend.Login()` — Mkdir error handling (not "already exists")
  - `MultiBackend.isAvailable()` — cooldown expiration path
  - `Envelope.Encode()` / `Decode()` — streaming I/O methods not directly tested
  - `Engine.gcLoop()` — tombstone TTL expiry edge cases
  - `Engine.pollLoop()` — empty poll backoff reset to minPollInterval
  - `pool.go` — only tested indirectly through engine_test.go
- **Architecture weaknesses**:
  - No retry logic for failed storage operations (upload, download, delete)
  - Memory buffering: entire proxied traffic buffered in memory (txBuf per session, full file content)
  - Double semaphore in download workers (see Backlog)
