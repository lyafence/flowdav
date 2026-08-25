# flowdav

A lightweight SOCKS5 proxy that uses WebDAV as a transport layer. Route your traffic through your home internet connection when connected to public Wi-Fi (cafe, hotel, etc.) by using WebDAV storage as an intermediary.

![Go](https://img.shields.io/badge/go-1.26.6-blue)
![License](https://img.shields.io/badge/license-MIT-green)
![Release](https://img.shields.io/github/v/release/lyafence/flowdav)

## Features

- **Zero open ports** — server has no listening ports for data; all communication happens through WebDAV storage (optional health endpoint on loopback)
- **End-to-end encryption** — AES-256-GCM + HMAC-SHA256 on all data, no plaintext on storage
- **TLS fingerprint masking** — uTLS masquerades as Chrome 133, browser User-Agent on all WebDAV requests
- **Adaptive polling** — idle backoff up to 60s with jitter, instant reset on activity, zero API calls when no sessions
- **Rate-limit protection** — HTTP 429 triggers separate cooldown, sessions auto-migrate to another backend
- **Multi-account rotation** — random session assignment, round-robin upload fallback across independent backends
- **DNS leak protection** — raw resolver on the client side, UDP explicitly blocked
- **Config encryption** — PBKDF2 + AES-256-GCM, secrets never stored in plaintext

## How It Works

```
[SOCKS5 Client] ←→ [flowdav -c] ←→ [WebDAV Storage] ←→ [flowdav -s] ←→ [Destination]
                   (encrypt, mux)  (passive store)     (decrypt, demux)
```

1. SOCKS5 client (browser/app) connects to flowdav client on `127.0.0.1:1080`
2. Client wraps data in encrypted envelopes (AES-256-GCM + HMAC-SHA256)
3. Client uploads encrypted data to WebDAV storage
4. Server polls WebDAV, downloads and decrypts envelopes
5. Server opens real TCP connections to the destination
6. Response flows back through WebDAV to the client

## Disclaimer

> **Disclaimer:** This project is provided AS IS, without warranty. It is designed for privacy protection on untrusted networks (e.g., public Wi-Fi) and as a proof-of-concept for data transport over WebDAV. Users are solely responsible for complying with applicable laws in their jurisdiction. The authors assume no liability for misuse or unlawful use.

## Quick Start

### What you need

- A WebDAV storage (any provider — rclone, NextCloud, ownCloud, or a dedicated WebDAV service)
- Two machines sharing the same WebDAV: **server** at home (connects to destinations), **client** at cafe (your proxy entry point). For testing, both can run on the same machine.

```bash
# 1. Install (auto-detect OS and architecture)
curl -sSf https://raw.githubusercontent.com/lyafence/flowdav/main/scripts/get-flowdav.sh | sh

# 2. Generate config (interactive — 3 prompts for URL, login, token)
./flowdav -g config.json

# 3. Start the server (at home, polls WebDAV)
./flowdav -s config.json

# 4. Start the client (at cafe, SOCKS5 on 127.0.0.1:1080)
./flowdav -c config.json

# 5. Test the proxy
curl -s --proxy socks5h://127.0.0.1:1080 https://api.ipify.org
```

All encryption keys are generated automatically. The binary generates fresh `enc_key`/`hmac_key` for you — no manual `openssl` needed.

> **Don't have two machines?** Run both on the same machine. **Windows?** Download from [Releases](https://github.com/lyafence/flowdav/releases).

## Configuration

For full control over every field, create or edit the config manually.

### Generate encryption keys

Keys must be identical on client and server:

```bash
openssl rand -base64 32  # enc_key
openssl rand -base64 32  # hmac_key
```

### Example config

```json
{
  "listen_addr": "127.0.0.1:1080",
  "webdav": {
    "url": "https://your-webdav:8080",
    "login": "username",
    "token": "YOUR_TOKEN",
    "base_path": "data_sync"
  },
  "enc_key": "paste enc_key here",
  "hmac_key": "paste hmac_key here"
}
```

Required: `webdav.url`, `webdav.login`, `webdav.token`, `enc_key`, `hmac_key`. See the [Config Reference](#config-reference) for all optional fields.

### Multi-backend

Replace the single backend with a `backends` array for account rotation:

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

### Encrypted configs

```bash
./flowdav -e config.json              # encrypt
./flowdav -c config.json.enc -p secret  # run encrypted
FLOWDAV_PASSWORD=secret ./flowdav -c config.json.enc  # or via env
```

## Docker

Images are published on [GitHub Container Registry](https://github.com/lyafence/flowdav/pkgs/container/flowdav).
Pass the desired mode (`-c`, `-s`, `-e`) as the command:

```bash
# start the server (at home)
docker run --rm -v ./config.json:/app/configs/config.json \
  ghcr.io/lyafence/flowdav flowdav -s /app/configs/config.json

# start the client (at cafe)
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
| `webdav` | object | — | ✓ | ✓ | WebDAV connection (see example) |
| `webdav.base_path` | string | `""` | ✓ | ✓ | WebDAV subdirectory for files |
| `enc_key` | string | — | ✓ | ✓ | 32-byte AES-256 key, base64 |
| `hmac_key` | string | — | ✓ | ✓ | 32-byte HMAC-SHA256 key, base64 |
| `listen_addr` | string | `"127.0.0.1:1080"` | ✓ | | SOCKS5 listener (`host:port`) |
| `log_level` | string | `"info"` | ✓ | ✓ | Log level (`debug`, `info`, `warn`, `error`) |
| `socks5_user` | string | `""` | ✓ | | SOCKS5 auth username |
| `socks5_pass` | string | `""` | ✓ | | SOCKS5 auth password |
| `max_connections` | int | `100` | ✓ | | Max concurrent SOCKS5 conns |
| `refresh_rate_ms` | int | `500` | ✓ | ✓ | Poll interval |
| `min_poll_ms` | int | `500` | ✓ | ✓ | Min poll interval (idle backoff floor) |
| `max_poll_ms` | int | `60000` | ✓ | ✓ | Max poll jitter ceiling (idle backoff) |
| `flush_rate_ms` | int | `500` | ✓ | ✓ | Flush interval |
| `max_sessions` | int | `0` (∞) | ✓ | ✓ | Max WebDAV sessions |
| `idle_timeout_ms` | int | `10000` | ✓ | ✓ | Session idle timeout (ms); 0 = default 10s |
| `max_message_size` | int | `16777216` | ✓ | ✓ | Max payload (bytes), capped at 1GB |
| `tls_fingerprint` | string | `"chrome"` | ✓ | ✓ | TLS fingerprint profile (`chrome`, `chrome_auto`) |
| `padding_size` | int | `0` (off) | ✓ | ✓ | Tail padding bucket size (bytes); hides exact payload size from storage observer. Adds overhead; try `4096` or `16384` with public WebDAV providers. |
| `hold_ms` | int | `0` (off) | | ✓ | Max server-side random delay before flushing (ms). Adds latency. |
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

> *Security reports are evaluated on engineering and protocol correctness.*

## Troubleshooting

- **First request may take a few seconds** — the client polls WebDAV every 500ms; idle backoff can increase latency on cold start.
- **HTTPS sites fail but HTTP works** — check DNS resolution from your server machine. The server resolves destination hostnames.
- **"Failed to load config"** — if the file is encrypted, use `-p` flag or `FLOWDAV_PASSWORD` env var. If not, check the JSON syntax.
- **Connection resets during active browsing** — enable debug logging with `-l debug` to see session-level errors.

## Android

Download `flowdav-android.apk` from [GitHub Releases](https://github.com/lyafence/flowdav/releases).

The app accepts an encrypted config file (`.json.enc`) via file picker, or manual WebDAV and encryption key fields.
SOCKS5 proxy runs on the configured address (default `127.0.0.1:1080`).

## Release Archives

Multi-platform release archives are built automatically by CI on each tag (`v*`).
Download the latest archive from [GitHub Releases](https://github.com/lyafence/flowdav/releases).

Each archive contains: a single `flowdav` binary (unified: client, server, encrypt), an example config (`flowdav.json.example`), and README.

Run `flowdav --version` to print the release version; `flowdav --help` for all modes.

## Similar Projects

- [webdav-tunnel](https://github.com/spkprsnts/webdav-tunnel) — TCP tunnel over WebDAV with self-hosted mode (embedded server) and yamux multiplexing. Uses a different wire protocol and is **not compatible** with flowdav.

## Development

This project was developed with AI assistance. All code is reviewed, tested,
and maintained by the author. Contributions are welcome regardless of how
they are produced — AI-assisted or hand-written.

## License

MIT — see [LICENSE](./LICENSE) for details.

Flowdav is an independent implementation built on the transport design of [NullLatency/FlowDriver](https://github.com/NullLatency/FlowDriver) (MIT); flowdav is released under its own terms.
