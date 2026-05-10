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
- Optional HTTP health endpoint: set `health_port` in config (e.g., `"127.0.0.1:9090"`) to enable `GET /health` returning JSON engine stats
- Data flow: `[SOCKS5] ←→ client ←→ WebDAV ←→ server ←→ destination`
  (client encrypts & muxes; server decrypts & demuxes; WebDAV is passive storage)
- Sessions are stored as `{dir}-{clientID}-{timestamp}.bin` (e.g., `rq-client1-1778180385216825104.bin`)
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
- **Unit tests**: `make test` (98+ tests across 7 packages, race-enabled)
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
- **Tests** (current):
  - `internal/config`: 33+ tests (Load, validation, crypto, encrypted config, ResolvePassword)
  - `internal/storage`: 10+ tests (fullPath, isLocalURL, validateNotPrivateURL, multi-backend)
  - `internal/transport`: 30+ tests (Session, Engine, Envelope, Crypto, VirtualConn, worker pool)
  - `internal/logger`: 4 tests (SetLevel, logging levels, filtering)
  - `internal/config/e2e_test.go`: E2E test for encrypted config flow (builds binaries, tests -p flag, FLOWDAV_PASSWORD, wrong password)
- **Refactoring completed**:
  - Worker pool in `engine.pollLoop` ✅
  - Deep copy of Envelope.Payload in session.rxQueue ✅
  - `sync.Once` for VirtualConn.Close() ✅
  - Circuit breaker in MultiBackend ✅
- **Known gaps**:
  - Wire version injection through CI and Makefile
  - Replace `sleep 10` in E2E tests with healthcheck polling
