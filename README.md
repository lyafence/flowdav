# flowdav

A lightweight SOCKS5 proxy that uses WebDAV as a transport layer. Route your traffic through your home internet connection when connected to public Wi-Fi (cafe, hotel, etc.) by using WebDAV storage as an intermediary.

![Go](https://img.shields.io/badge/go-1.27.3-blue)
![License](https://img.shields.io/badge/license-MIT-green)
![Release](https://img.shields.io/github/v/release/lyafence/flowdav)

## Features

- **Zero open ports** — server has no listening ports for data; all communication happens through WebDAV storage (optional health endpoint on loopback)
- **End-to-end encryption** — AES-256-GCM + HMAC-SHA256 on all data, no plaintext on storage
- **TLS fingerprint masking** — uTLS masquerades as Chrome 133, browser User-Agent on all WebDAV requests
- **Adaptive polling** — idle backoff up to 60s with jitter, instant reset on activity; an idle client with no sessions makes zero API calls
- **Rate-limit protection** — HTTP 429 triggers separate cooldown, sessions auto-migrate to another backend
- **Multi-account rotation** — random session assignment, round-robin upload fallback across independent backends
- **DNS leak protection** — raw resolver on the client side, UDP explicitly blocked
- **Config encryption** — PBKDF2 + AES-256-GCM, secrets never stored in plaintext
- **QR config import** — `-e --qr` prints a scannable code; the Android app imports it in one tap (no secrets in plaintext, only the encrypted config)
- **Connection test** — the Android app can verify the tunnel end to end. Its final (informational) stage probes a shuffled list of public IP-echo services *through the tunnel* and shows the server's exit IP; the deterministic verdict comes from the storage round-trip

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

> **One keypair serves exactly one client.** Client and server must share the same `enc_key`/`hmac_key`, but only **one** client may be active per keypair + WebDAV `base_path`. A second client with the same keys would race the first for the server's response files and consume/destroy them (the client deletes every response it downloads, including packets for sessions it does not own). To protect several devices, run a separate client/server pair per device — each with its own keypair and an isolated `base_path`.

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

Required: `webdav.url` (or `webdav.backends`), `enc_key`, `hmac_key`. `webdav.login` and `webdav.token` are required only if the WebDAV server needs authentication. See the [Config Reference](#config-reference) for all optional fields.

### Strict config validation

Configs with unknown JSON fields are rejected. This catches typos such as
`socks5_username` (correct: `socks5_user`) before they can silently disable a setting, but it also means
extra/legacy keys must be removed when upgrading. The error looks like
`json: unknown field "socks5_username"`. The same validation runs for `-c`,
`-s`, and `-e` (before the `.enc` file is written).

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
./flowdav -c config.json.enc          # run encrypted (prompts for password)
FLOWDAV_PASSWORD=secret ./flowdav -c config.json.enc  # or via env

./flowdav --qr -e config.json             # encrypt + print a QR code for the Android app
./flowdav --remove-source -e config.json  # delete the plaintext after encrypting
./flowdav -g -e --qr -p secret config.json  # generate, encrypt and show the QR in one go
```

Scanning the QR with the Android app imports the encrypted config directly; the password is requested when you start the proxy.

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

# encrypt an existing config (mount the directory so config.json.enc persists)
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/app/configs" \
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
| `refresh_rate_ms` | int | `500` | ✓ | ✓ | Legacy base poll interval (alias for `min_poll_ms`; explicit `min_poll_ms` wins) |
| `min_poll_ms` | int | `500` | ✓ | ✓ | Min poll interval (idle backoff floor) |
| `max_poll_ms` | int | `60000` | ✓ | ✓ | Max poll interval (backoff ceiling) |
| `flush_rate_ms` | int | `500` | ✓ | ✓ | Flush interval |
| `max_sessions` | int | `0` (∞) | | ✓ | Max concurrent server-side WebDAV sessions (clients use `max_connections`) |
| `idle_timeout_ms` | int | `10000` | ✓ | ✓ | Session idle timeout (ms); 0 = default 10s |
| `max_message_size` | int | `16777216` | ✓ | ✓ | Max payload (bytes), capped at 1GB |
| `tls_fingerprint` | string | `""` (Chrome 133) | ✓ | ✓ | TLS fingerprint profile (`chrome`, `chrome_auto`) |
| `padding_size` | int | `0` (off) | ✓ | ✓ | Tail padding bucket size (bytes); hides exact payload size from storage observer. Adds overhead; try `4096` or `16384` with public WebDAV providers. |
| `hold_ms` | int | `0` (off) | | ✓ | Max server-side random delay before flushing (ms). Adds latency. |
| `health_port` | string | `""` | ✓ | ✓ | Health endpoint (`host:port`) |

Client-only fields (`listen_addr`, `socks5_user`, `socks5_pass`, `max_connections`) are absent from server configs. Unset fields use defaults. `socks5_user` and `socks5_pass` must be set together — a partial pair is rejected, since it would silently disable SOCKS5 authentication.

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

- **First request may take a few seconds** — the client polls WebDAV every 500ms; idle backoff can increase latency on cold start. Run **Test connection** to wake the server: the second run is fast.
- **HTTPS sites fail but HTTP works** — check DNS resolution from your server machine. The server resolves destination hostnames.
- **"json: unknown field ..."** — remove the extra/typo key from the config; strict parsing rejects unknown fields for `-c`, `-s`, and `-e`.
- **"Failed to load config"** — if the file is encrypted, use `-p` flag or `FLOWDAV_PASSWORD` env var. If not, check the JSON syntax.
- **Connection resets during active browsing** — enable debug logging with `-l debug` to see session-level errors.
- **Android: proxy stops in the background** — allow the app to ignore battery optimizations (prompted on first start) and enable "Start on boot" in the Advanced section.

## Android

Download `flowdav-android-<tag>.apk` from [GitHub Releases](https://github.com/lyafence/flowdav/releases).

The app accepts a config in three ways: scan a QR code (`./flowdav --qr -e config.json`), import from clipboard or another app's Share menu, or pick a file (`.json` / `.json.enc`) — manual WebDAV and key fields are also available. Manual mode uses an empty `base_path` and default tuning; use a config file/QR for a non-empty `base_path`.

Highlights:

- **Quick Settings tile** — start/stop the proxy with one tap from the notification shade.
- **Keep-alive** — foreground service with bounded automatic reconnect, plus optional battery-optimization exemption and **Start on boot** (Advanced section).
- **Test connection** — one tap quickly verifies the local proxy and a WebDAV storage round-trip, then proves the full path through the tunnel (client → WebDAV → server → internet) and shows the server's exit IP. The end-to-end check may take up to the probe timeout; on a cold tunnel the server may be in idle backoff, so a second tap usually completes it.
- **How to use** — tap the ⓘ next to the status to get the setup steps; then copy the proxy address from the dashboard (tap it) and paste it into an app that supports SOCKS5.

When a config is encrypted, the master password is stored (Keystore-encrypted) only after a successful start; **Forget saved password** clears it.

## Release Archives

Multi-platform release archives are built automatically by CI on each tag (`v*`).
Download the latest archive from [GitHub Releases](https://github.com/lyafence/flowdav/releases).

Each archive contains: a single `flowdav` binary (unified: client, server, encrypt), an example config (`flowdav.json.example`), README, and LICENSE.

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
