# flowdav

A lightweight SOCKS5 proxy that uses WebDAV as a transport layer. Route your traffic through your home internet connection when connected to public Wi-Fi (cafe, hotel, etc.) by using WebDAV storage as an intermediary.

**Inspired by [NullLatency/FlowDriver](https://github.com/NullLatency/FlowDriver) — credit to NullLatency for the original concept.**

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
- Default SOCKS5 port is 1080 (`127.0.0.1`)

> **Disclaimer:** This tool is designed for legitimate privacy protection — securing traffic on untrusted public Wi-Fi networks. Users are solely responsible for complying with all applicable laws in their jurisdiction. The authors assume no liability for misuse or unlawful use.

## Quick Start

### What you need

- A WebDAV storage (any provider — rclone, NextCloud, ownCloud, or a dedicated WebDAV service)
- Two machines sharing the same WebDAV: **server** at home (connects to destinations), **client** at cafe (your proxy entry point). For testing, both can run on the same machine.

### 1. Install

#### Option A — Binary archive

Download the latest archive from [GitHub Releases](https://github.com/lyafence/flowdav/releases):

```bash
tar -xzf flowdav-*.tar.gz
cd flowdav-*/
```

### 2. Configure

#### Generate encryption keys

The `enc_key` and `hmac_key` must be identical on client and server:

```bash
head -c 32 /dev/urandom | base64 -w0; echo  # enc_key
head -c 32 /dev/urandom | base64 -w0; echo  # hmac_key
```

#### Copy example configs

```bash
cp flowdav_server.json.example server.json
cp flowdav_client.json.example client.json
```

#### Edit the server config

The server runs at home and opens real TCP connections. It has no `listen_addr`:

```json
{
  "storage_type": "webdav",
  "webdav": {
    "url": "https://your-webdav-server:8080",
    "login": "username",
    "token": "YOUR_WEBDAV_TOKEN"
  },
  "enc_key": "paste enc_key here",
  "hmac_key": "paste hmac_key here"
}
```

#### Edit the client config

The client runs at cafe and listens for SOCKS5 connections:

```json
{
  "listen_addr": "127.0.0.1:1080",
  "storage_type": "webdav",
  "webdav": {
    "url": "https://your-webdav-server:8080",
    "login": "username",
    "token": "YOUR_WEBDAV_TOKEN"
  },
  "enc_key": "paste enc_key here",
  "hmac_key": "paste hmac_key here"
}
```

> **Key difference:** client has `listen_addr` (SOCKS5 port); server does not.

All configs support optional fields: `max_message_size` (default 16MB), `max_sessions` (default 0 = unlimited), `health_port` (default: disabled). See the [Config Reference](#config-reference) table for the full list. Both `--version` and `-l <level>` flags are supported on all three binaries.

For multiple WebDAV providers (round-robin), replace the single backend fields with a `backends` array:

```json
{
  "webdav": {
    "backends": [
      { "url": "https://webdav1.example.com", "login": "user", "token": "pass" },
      { "url": "https://webdav2.example.com", "login": "user", "token": "pass" }
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

### 4. Docker

Images are published on [GitHub Container Registry](https://github.com/lyafence/flowdav/pkgs/container/flowdav).
Run any binary by passing it as the command (`docker run` pulls automatically if not cached):

```bash
# start the server (at home)
docker run --rm -v ./server.json:/app/configs/config.json \
  ghcr.io/lyafence/flowdav flowdav-server -c /app/configs/config.json

# start the client (at cafe), exposes SOCKS5 on 127.0.0.1:1080
docker run --rm -v ./client.json:/app/configs/config.json \
  ghcr.io/lyafence/flowdav flowdav-client -c /app/configs/config.json

# generate encrypted config
docker run --rm -i ghcr.io/lyafence/flowdav flowdav-encrypt --gen-keys < server.json > server.json.enc

# run with encrypted config
docker run --rm -v ./server.json.enc:/app/configs/server.json.enc \
  -e FLOWDAV_PASSWORD=secret \
  ghcr.io/lyafence/flowdav flowdav-server -p -c /app/configs/server.json.enc
```

## Config Files

| File | Type | listen_addr | Health Port |
|------|------|-------------|-------------|
| `flowdav_client.json.example` | Client | `127.0.0.1:1080` | — |
| `flowdav_server.json.example` | Server | No | — |

### Config Reference

| Field | Type | Default | Client | Server | Description |
|-------|------|---------|--------|--------|-------------|
| `storage_type` | string | `"webdav"` | ✓ | ✓ | Backend type |
| `webdav` | object | — | ✓ | ✓ | WebDAV connection (see example) |
| `webdav.base_path` | string | `""` | ✓ | ✓ | WebDAV subdirectory for files |
| `enc_key` | string | — | ✓ | ✓ | 32-byte AES-256 key, base64 |
| `hmac_key` | string | — | ✓ | ✓ | 32-byte HMAC-SHA256 key, base64 |
| `listen_addr` | string | — | ✓ | | SOCKS5 listener (`host:port`) |
| `log_level` | string | `"info"` | ✓ | ✓ | Log level (`debug`, `info`, `warn`, `error`) |
| `socks5_user` | string | `""` | ✓ | | SOCKS5 auth username |
| `socks5_pass` | string | `""` | ✓ | | SOCKS5 auth password |
| `max_connections` | int | `100` | ✓ | | Max concurrent SOCKS5 conns |
| `refresh_rate_ms` | int | `500` | ✓ | ✓ | Poll interval |
| `min_poll_ms` | int | `100` | ✓ | ✓ | Min poll jitter floor |
| `max_poll_ms` | int | `5000` | ✓ | ✓ | Max poll jitter ceiling |
| `flush_rate_ms` | int | `500` | ✓ | ✓ | Flush interval |
| `max_sessions` | int | `0` (∞) | ✓ | ✓ | Max WebDAV sessions |
| `max_message_size` | int | `16777216` | ✓ | ✓ | Max payload (bytes) |
| `health_port` | string | `""` | ✓ | ✓ | Health endpoint (`host:port`) |

Client-only fields (`listen_addr`, `socks5_user`, `socks5_pass`, `max_connections`) are absent from server configs. Unset fields use defaults.

## Health Check

Both the client and server support an optional HTTP health endpoint. Set `health_port` in the config to enable it (e.g., `"127.0.0.1:9191"`). The endpoint `GET /health` returns JSON with engine statistics:

```json
{
  "active_sessions": 0,
  "upload_retries": 0,
  "download_retries": 0,
  "tx_queue_bytes": 0,
  "tx_queue_sessions": 0,
  "poll_ticker_ms": 500,
  "flush_ticker_ms": 500,
  "role": "client",
  "backends": [
    {"url": "http://webdav1:8080", "available": true, "failures": 0}
  ]
}
```

- `active_sessions` / `closed_sessions` — current and completed WebDAV sessions.
- `upload_retries` / `download_retries` — cumulative storage retry counters (reset on restart).
- `tx_queue_bytes` / `tx_queue_sessions` — transmit buffer backpressure: how much data is waiting to be uploaded.
- `backends` — per-backend health for multi-WebDAV setups (circuit breaker state). Omitted for single-backend configs.

## Security

- **Encryption:** AES-256-GCM + HMAC-SHA256 (configured in config.json)
- **SOCKS5 authentication:** username/password (if specified in config.json)
- **DNS leak protection:** Raw resolver (no local DNS lookups)
- **UDP blocked:** Only TCP traffic is supported

## Android

Download `flowdav-android.apk` from [GitHub Releases](https://github.com/lyafence/flowdav/releases).

The app accepts `client.json.enc` or manual WebDAV and encryption key fields.
SOCKS5 proxy runs on `0.0.0.0:1080` — configure your browser manually.

## Release Archives

Multi-platform release archives are built automatically by CI on each tag (`v*`).
Download the latest archive from [GitHub Releases](https://github.com/lyafence/flowdav/releases).

Each archive contains: `flowdav-client`, `flowdav-server`, `flowdav-encrypt`, two example configs (`flowdav_client.json.example`, `flowdav_server.json.example`), and README.

All binaries accept `--version` to print the release version.

## License

MIT — see [LICENSE](./LICENSE) for details.

Flowdav is an independent implementation inspired by the concept of [NullLatency/FlowDriver](https://github.com/NullLatency/FlowDriver).
The original project does not specify a license; flowdav is released under its own terms.
