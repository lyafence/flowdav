# flowdav

A lightweight SOCKS5 proxy that uses WebDAV as a transport layer. Route your traffic through your home internet connection when connected to public Wi-Fi (cafe, hotel, etc.) by using WebDAV storage as an intermediary.

**Inspired by [NullLatency/FlowDriver](https://github.com/NullLatency/FlowDriver) — credit to NullLatency for the original concept.**

## How It Works

```
[SOCKS5 Client] ←→ [flowdav -c] ←→ [WebDAV Storage] ←→ [flowdav -s] ←→ [Destination]
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
- Adaptive polling: idle backoff up to 60s with ±75% jitter, instant reset on activity
- TLS fingerprint masking (Chrome 133) + browser User-Agent
- 429-aware circuit breaker (60s cooldown, session migration)
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

#### Copy the example config

```bash
cp flowdav.json.example config.json
```

#### Edit the config

A single config works for both server and client — each binary ignores fields it doesn't need:

```json
{
  "listen_addr": "127.0.0.1:1080",
  "webdav": {
    "url": "https://your-webdav-server:8080",
    "login": "username",
    "token": "YOUR_WEBDAV_TOKEN"
  },
  "enc_key": "paste enc_key here",
  "hmac_key": "paste hmac_key here"
}
```

The server ignores `listen_addr`; both the client and server support `health_port` (see below).

Optional fields: `max_message_size` (default 16MB), `max_sessions` (default 0 = unlimited), `health_port` (default: disabled). See the [Config Reference](#config-reference) table for the full list. Both `--version` and `-l <level>` flags are supported on the unified binary.

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

See `flowdav.json.example` for the single-backend structure.

#### Optional: encrypt configs

Encrypt your config with a master password so secrets are never stored in plaintext:

```bash
# Option 1: Generate a config from scratch (interactive prompts):
./flowdav -g config.json

# Option 2: Encrypt an existing config with a master password:
./flowdav -e config.json                 # prompts for password
./flowdav -e config.json -p secret       # or via flag
FLOWDAV_PASSWORD=secret ./flowdav -e config.json  # or via env

# Option 3: Generate and encrypt in one step:
./flowdav -g -e config.json              # prompts for WebDAV + password
```

Run with the encrypted config:

```bash
./flowdav -s config.json.enc             # prompts for password
./flowdav -c config.json.enc -p secret   # or via flag
FLOWDAV_PASSWORD=secret ./flowdav -c config.json.enc  # or via env
```

### 3. Run

**Start the server** (at home):

```bash
./flowdav -s config.json
# or with encrypted config:
./flowdav -s config.json.enc             # prompts for password
```

The server polls WebDAV for new sessions — no listening ports required.

**Start the client** (at cafe):

```bash
./flowdav -c config.json
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
Pass the desired mode (`-c`, `-s`, `-e`) as the command (`docker run` pulls automatically if not cached):

```bash
# start the server (at home)
docker run --rm -v ./config.json:/app/configs/config.json \
  ghcr.io/lyafence/flowdav flowdav -s /app/configs/config.json

# start the client (at cafe), exposes SOCKS5 on 127.0.0.1:1080
docker run --rm -v ./config.json:/app/configs/config.json \
  ghcr.io/lyafence/flowdav flowdav -c /app/configs/config.json

# encrypt an existing config
docker run --rm -v ./config.json:/app/configs/config.json \
  -e FLOWDAV_PASSWORD=secret \
  ghcr.io/lyafence/flowdav flowdav -e /app/configs/config.json

# run with encrypted config
docker run --rm -v ./config.json.enc:/app/configs/config.json.enc \
  -e FLOWDAV_PASSWORD=secret \
  ghcr.io/lyafence/flowdav flowdav -c /app/configs/config.json.enc
```

## Config Files

| File | Type | listen_addr | Health Port |
|------|------|-------------|-------------|
| `flowdav.json.example` | Universal | `127.0.0.1:1080` | — |

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
| `max_poll_ms` | int | `60000` | ✓ | ✓ | Max poll jitter ceiling (idle backoff) |
| `flush_rate_ms` | int | `500` | ✓ | ✓ | Flush interval |
| `max_sessions` | int | `0` (∞) | ✓ | ✓ | Max WebDAV sessions |
| `max_message_size` | int | `16777216` | ✓ | ✓ | Max payload (bytes) |
| `tls_fingerprint` | string | `"chrome"` | ✓ | ✓ | TLS fingerprint profile (`chrome`, `chrome_auto`) |
| `health_port` | string | `""` | ✓ | ✓ | Health endpoint (`host:port`) |

Client-only fields (`listen_addr`, `socks5_user`, `socks5_pass`, `max_connections`) are absent from server configs. Unset fields use defaults.

## Health Check

Both the client and server support an optional HTTP health endpoint. Set `health_port` in the config to enable it (e.g., `"127.0.0.1:9191"`). The endpoint `GET /health` returns JSON with engine statistics:

```json
{
  "active_sessions": 0,
  "closed_sessions": 0,
  "processed_files": 0,
  "upload_retries": 0,
  "download_retries": 0,
  "tx_queue_bytes": 0,
  "tx_queue_sessions": 0,
  "poll_ticker_ms": 500,
  "flush_ticker_ms": 500,
  "role": "client",
  "backends": [
    {"url": "http://webdav1:8080", "available": true, "failures": 0, "rate_limited": false, "rate_limit_remain_sec": 0}
  ]
}
```

- `active_sessions` / `closed_sessions` — current and completed WebDAV sessions.
- `upload_retries` / `download_retries` — cumulative storage retry counters (reset on restart).
- `tx_queue_bytes` / `tx_queue_sessions` — transmit buffer backpressure: how much data is waiting to be uploaded.
- `backends` — per-backend health for multi-WebDAV setups (circuit breaker + rate-limit state). Omitted for single-backend configs.

## Security

- **Encryption:** AES-256-GCM + HMAC-SHA256 (configured in config.json)
- **SOCKS5 authentication:** username/password (if specified in config.json)
- **DNS leak protection:** Raw resolver (no local DNS lookups)
- **UDP blocked:** Only TCP traffic is supported

## Android

Download `flowdav-android.apk` from [GitHub Releases](https://github.com/lyafence/flowdav/releases).

The app accepts an encrypted config file (`.json.enc`) via file picker, or manual WebDAV and encryption key fields.
SOCKS5 proxy runs on the configured address (default `127.0.0.1:1080`).

## Release Archives

Multi-platform release archives are built automatically by CI on each tag (`v*`).
Download the latest archive from [GitHub Releases](https://github.com/lyafence/flowdav/releases).

Each archive contains: a single `flowdav` binary (unified: client, server, encrypt), an example config (`flowdav.json.example`), and README.

Run `flowdav --version` to print the release version; `flowdav --help` for all modes.

## License

MIT — see [LICENSE](./LICENSE) for details.

Flowdav is an independent implementation inspired by the concept of [NullLatency/FlowDriver](https://github.com/NullLatency/FlowDriver).
The original project does not specify a license; flowdav is released under its own terms.
