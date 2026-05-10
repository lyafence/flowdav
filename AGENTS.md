# flowdav - Agent Instructions

**Inspired by [NullLatency/FlowDriver](https://github.com/NullLatency/FlowDriver) — credit to NullLatency for the original concept.**
Flowdav is an independent implementation; the original project does not specify a license.

## Build
```bash
# Build linux binaries in podman, copy to ./bin
./scripts/build.sh --image-to-bin

# Build release archives (multi-platform zips in ./release)
./scripts/build.sh
```

## Run
- Configs: `configs/flowdav_{role}.json.example` (client has `listen_addr`, server does not)
- Flags: `-c` config, `-l` log level (debug|info|warn|error)
- Health endpoint: `GET /health` returns JSON stats (active sessions, processed files, role, rates)

## Architecture
- Server has **no listening ports** for data — all communication via WebDAV storage
- Optional HTTP health endpoint: set `health_port` in config (e.g., `"127.0.0.1:9090"`) to enable `GET /health` returning JSON engine stats
- Data flow: `[SOCKS5] → client → WebDAV → server → destination`
- Sessions are stored as `{dir}-{clientID}-{timestamp}.bin` (e.g., `rq-client1-1778180385216825104.bin`)
- Encryption: AES-256-GCM + HMAC-SHA256 (via `enc_key`/`hmac_key`)
- DNS leak protection: client uses raw resolver (no local DNS lookups)
- UDP explicitly blocked

## Storage Backends
- **WebDAV only**: `storage_type: "webdav"` with `provider: "custom"`, `url`, `login`, `token`. 
- **Multi-WebDAV**: Support for multiple WebDAV providers with round-robin session assignment. Configure via `webdav.backends` array in the config.
  - Example:
    ```json
    "webdav": {
      "backends": [
        {
          "provider": "custom",
          "url": "http://webdav1:8080",
          "login": "user1",
          "token": "pass1",
          "base_path": "app1"
        },
        {
          "provider": "custom",
          "url": "http://webdav2:8080",
          "login": "user2",
          "token": "pass2",
          "base_path": "app2"
        }
      ]
    }
    ```
- Test with: `podman run -d --name webdav-test -p 8080:8080 docker.io/rclone/rclone:latest serve webdav /data --addr 0.0.0.0:8080 --user test --pass test`
- Local testing removed - use WebDAV test setup.

## Testing
- **Unit tests exist** for `config`, `storage`, `transport/crypto`, `transport/envelope`, `logger` packages
- **E2E test**: `scripts/test_proxy_comprehensive.sh`
- **Manual WebDAV test with podman-compose**:
  1. `podman-compose -f docker-compose.yml up -d`
  2. All services: webdav-test, flow-server, flow-client
  3. Client must show `Listening for SOCKS5 on 0.0.0.0:11080...` and stay running
  4. **Important**: volumes in docker-compose.yml need `:Z` suffix for SELinux (e.g., `./configs/flowdav_test_server.json:/app/configs/flowdav_test_server.json:Z`)
  5. Test proxy: `podman run --rm --network flowdav_flow-net alpine:latest sh -c "apk add --no-cache curl && curl --socks5h-hostname flow-client:11080 https://api.ipify.org"`

## Notes
- Go 1.26.2 (see `go.mod`)
- Client handles SIGINT/SIGTERM: closes listener and cancels context for graceful shutdown
- SOCKS5 authentication: set `socks5_user` and `socks5_pass` in config for production

## Security
- Encryption: AES-256-GCM + HMAC-SHA256 (mandatory)
- SOCKS5 auth: username/password (recommended for production)
- All traffic encrypted in WebDAV storage
- Server has no exposed ports - reduced attack surface

## Technical Debt

- **Unit Tests**: Tests exist but coverage should be expanded:
  - `internal/config`: 7 tests (Load, validation, error handling) ✅
  - `internal/storage`: 9 tests (fullPath, isLocalURL, validateNotPrivateURL) ✅
  - `internal/transport`: 12+ tests (Session, Engine, Envelope, Crypto) ✅
  - `internal/logger`: 3 tests (SetLevel, logging levels) ✅

  - **Refactoring**:
  - Add worker pool in `engine.pollLoop` to limit goroutines (HIGH-2) ✅
  - Deep copy of Envelope.Payload in session.rxQueue (HIGH-4) ✅
  - Replace `select{}` with proper signal handling ✅

- **Documentation**:
  - Clarify port differences (1080 default vs 11080 in docker-compose).
  - **Metrics & Observability**:
  - Add Prometheus `/metrics` endpoint alongside `/health`.
  - Expose: per-backend latency histograms, packet drop counter, active sessions gauge.
  - Effort: ~4h (add `prometheus/client_golang`, wire into Engine, register in main).
