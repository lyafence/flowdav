# flowdav

A lightweight SOCKS5 proxy that uses WebDAV as a transport layer. Route your traffic through your home internet connection when connected to public Wi-Fi (cafe, hotel, etc.) by using WebDAV storage as an intermediary.

**Inspired by [NullLatency/FlowDriver](https://github.com/NullLatency/FlowDriver) - credit to NullLatency for the original concept.**

## How It Works

```
[SOCKS5 Client] ←→ [flowdav-client] ←→ [WebDAV Storage] ←→ [flowdav-server] ←→ [Destination]
                   (encrypt, mux)      (passive store)     (decrypt, demux)
```

**Data flow:**
1. SOCKS5 client (browser/app) connects to client on `127.0.0.1:1080`
2. Client wraps data in `Envelope` (binary protocol), encrypts with AES-256-GCM + HMAC-SHA256
3. Client uploads encrypted data to WebDAV storage
4. Server polls WebDAV, downloads and decrypts envelopes
5. Server opens real TCP connections to destination
6. Response flows back through WebDAV to client

**Key points:**
- Server has no listening ports for data — all communication happens via WebDAV storage (optional health endpoint on loopback)
- Sessions use random filenames `{dir_byte}{16_hex}` (direction byte + random hex, no client ID or timestamp leakage)
- Client and server must use the same WebDAV storage and credentials
- Default SOCKS5 port is 1080 (127.0.0.1), docker-compose exposes as 11080 (0.0.0.0)

> **Disclaimer:** This tool is designed for legitimate privacy protection — securing traffic on untrusted public Wi-Fi networks. Users are solely responsible for complying with all applicable laws in their jurisdiction. The authors assume no liability for misuse or unlawful use.

## Quick Start

### What you need

- A WebDAV storage (any provider — rclone, NextCloud, ownCloud, or a dedicated WebDAV service)
- Two machines sharing the same WebDAV: **server** at home (connects to destinations), **client** at cafe (your proxy entry point). For testing, both can run on the same machine.

### 1. Install

**From a release archive** (recommended for end users):

```bash
# Download from https://github.com/lyafence/flowdav/releases
tar -xzf flowdav-*.tar.gz
cd flowdav-*/
```

**From source** (for development):

```bash
make build
# Binaries are in ./bin/
```

### 2. Configure

#### Generate encryption keys

The `enc_key` and `hmac_key` must be identical on client and server:

```bash
head -c 32 /dev/urandom | base64 -w0; echo  # enc_key
head -c 32 /dev/urandom | base64 -w0; echo  # hmac_key
```

#### Copy example configs

Paths depend on how you installed:

```bash
# From source checkout:
cp configs/flowdav_server.json.example configs/server.json
cp configs/flowdav_client.json.example configs/client.json

# From a release archive (configs are alongside binaries):
cp flowdav_server.json.example server.json
cp flowdav_client.json.example client.json
```

#### Edit the server config

The server runs at home and opens real TCP connections. It has no `listen_addr`:

```json
{
  "storage_type": "webdav",
  "webdav": {
    "provider": "custom",
    "url": "https://your-webdav-server:8080",
    "login": "username",
    "token": "password",
    "base_path": "data_sync"
  },
  "enc_key": "paste enc_key here",
  "hmac_key": "paste hmac_key here",
  "refresh_rate_ms": 500,
  "flush_rate_ms": 500,
  "log_level": "info",
  "health_port": "127.0.0.1:9190"
}
```

#### Edit the client config

The client runs at cafe and listens for SOCKS5 connections:

```json
{
  "listen_addr": "127.0.0.1:1080",
  "storage_type": "webdav",
  "webdav": {
    "provider": "custom",
    "url": "https://your-webdav-server:8080",
    "login": "username",
    "token": "password",
    "base_path": "data_sync"
  },
  "enc_key": "paste enc_key here",
  "hmac_key": "paste hmac_key here",
  "refresh_rate_ms": 500,
  "flush_rate_ms": 500,
  "log_level": "info",
  "socks5_user": "",
  "socks5_pass": "",
  "health_port": "127.0.0.1:9191"
}
```

> **Key differences between configs:** client has `listen_addr` (SOCKS5 port) and optional `socks5_user`/`socks5_pass`; server does not.

All configs support an optional `max_message_size` field (default 16777216 bytes / 16MB) to limit envelope payload size. Both `--version` and `-l <level>` flags are supported on all three binaries.

For multiple WebDAV providers (round-robin), replace the single backend fields with a `backends` array:

```json
{
  "webdav": {
    "backends": [
      { "url": "https://webdav1.example.com", "login": "user", "token": "pass", "base_path": "data_sync" },
      { "url": "https://webdav2.example.com", "login": "user", "token": "pass", "base_path": "data_sync" }
    ]
  }
}
```

See `flowdav_client.json.example` and `flowdav_server.json.example` for the single-backend structure.

#### Optional: encrypt configs

Encrypt your config with a master password so secrets are never stored in plaintext:

```bash
# Generate encryption keys automatically and encrypt:
FLOWDAV_PASSWORD=secret ./flowdav-encrypt --gen-keys < server.json > server.json.enc
FLOWDAV_PASSWORD=secret ./flowdav-encrypt --gen-keys < client.json > client.json.enc

# Or encrypt an already-configured file (keys already set):
FLOWDAV_PASSWORD=secret ./flowdav-encrypt < server.json > server.json.enc
```

Run with the encrypted config:

```bash
./flowdav-server -p -c server.json.enc   # prompts for password
# or via env var:
FLOWDAV_PASSWORD=secret ./flowdav-client -c client.json.enc
```

### 3. Run

**Start the server** (at home):

```bash
./flowdav-server -c server.json
# or with encrypted config:
./flowdav-server -p -c server.json.enc   # prompts for password
```

The server polls WebDAV for new sessions — no listening ports required.

**Start the client** (at cafe):

```bash
./flowdav-client -c client.json
```

The client listens on `127.0.0.1:1080` (or the address in `listen_addr`).

**Test the proxy:**

```bash
curl -s --proxy socks5h://127.0.0.1:1080 https://api.ipify.org
```

Or set it system-wide:

```bash
export ALL_PROXY=socks5://127.0.0.1:1080
```

### 4. Docker Compose (for testing only)

Quick full-stack test with three WebDAV backends and multi-client support:

```bash
# 1. Generate test configs with fresh keys
./scripts/prepare_test_env.sh

# 2. Build image
make docker-build

# 3. Start all services and run tests
./scripts/test_e2e.sh

# Or all at once:
make docker-e2e
```

Test the proxy after compose starts:

```bash
# Single-backend proxy (maps to docker-compose flow-client)
curl -s --proxy socks5h://127.0.0.1:11080 https://api.ipify.org

# Multi-backend proxy (maps to docker-compose flow-client-multi)
curl -s --proxy socks5h://127.0.0.1:11081 https://api.ipify.org
```

> **Port note:** The default SOCKS5 port is 1080 (localhost only). Docker Compose exposes the single-backend proxy as `11080` and multi-backend as `11081` on `0.0.0.0` to avoid conflicts and allow host access.

## Config Files

| File | Type | listen_addr | Health Port | Storage |
|------|------|-------------|-------------|---------|
| `flowdav_client.json.example` | Client | `127.0.0.1:1080` | `127.0.0.1:9191` | WebDAV |
| `flowdav_server.json.example` | Server | No | `127.0.0.1:9190` | WebDAV |

**Rule:** Client has `listen_addr` (SOCKS5 port), server does NOT (only polls WebDAV).

## Health Check

Both the client and server support an optional HTTP health endpoint. Set `health_port` in the config to enable it (e.g., `"127.0.0.1:9191"`). The endpoint `GET /health` returns JSON with engine statistics (active sessions, processed files, role, poll/flush rates).

In docker-compose, `HEALTHCHECK` is configured for both `flow-server` (port `9190`) and `flow-client` (port `9191`), enabling `depends_on` conditions and container orchestration health awareness.

## Security

- **Encryption:** AES-256-GCM + HMAC-SHA256 (configured in config.json)
- **SOCKS5 authentication:** username/password (if specified in config.json)
- **DNS leak protection:** Raw resolver (no local DNS lookups)
- **UDP blocked:** Only TCP traffic is supported

## Testing

E2E tests require built binaries in `bin/` and podman:

```bash
# 1. Generate test configs with fresh keys
./scripts/prepare_test_env.sh

# 2. Build Docker image
make docker-build

# 3. Start services and run tests
./scripts/test_e2e.sh

# Or run all at once:
make docker-e2e
```

### Release Archives

Multi-platform release archives are built with:

```bash
make release
```

Each archive contains: `flowdav-client`, `flowdav-server`, `flowdav-encrypt`, example configs, and README.

All binaries accept `--version` to print the release version.

## License

MIT — see [LICENSE](./LICENSE) for details.

Flowdav is an independent implementation inspired by the concept of [NullLatency/FlowDriver](https://github.com/NullLatency/FlowDriver).
The original project does not specify a license; flowdav is released under its own terms.
