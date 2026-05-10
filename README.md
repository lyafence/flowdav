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
- Server has no listening port — all communication happens via WebDAV storage
- Sessions are stored as `{dir}-{clientID}-{timestamp}.bin` (e.g., `rq-client1-1778180385216825104.bin`)
- Client and server must use the same WebDAV storage and credentials
- Default SOCKS5 port is 1080 (127.0.0.1), docker-compose exposes as 11080 (0.0.0.0)

## Quick Start

### Build

```bash
# Quick build (binaries to ./bin)
make build

# Or build in Podman and extract
make image-to-bin

# Build release archives (multi-platform .tar.gz)
make release
```

### Configuration

Copy config for production (example configs show the structure):

```bash
# Note: .gitignore excludes *.json to protect secrets.
# Only configs matching *example.json are tracked.
# Copy the example as your starting point:
cp configs/flowdav_client.json.example configs/client.json
cp configs/flowdav_server.json.example configs/server.json
```

**Note:** For testing with docker-compose, no copy is needed - the test configs are already referenced in `docker-compose.yml`.

Edit `configs/client.json`:

For a single WebDAV provider:
```json
{
  "listen_addr": "127.0.0.1:1080",
  "storage_type": "webdav",
  "webdav": {
    "provider": "custom",
    "url": "http://your-webdav-server:8080",
    "login": "username",
    "token": "password",
    "base_path": "myapp"
  },
  "enc_key": "your-aes-256-key-base64==",
  "hmac_key": "your-hmac-sha256-key-base64==",
  "socks5_user": "admin",
  "socks5_pass": "secret",
  "health_port": "127.0.0.1:9090"
}
```

For multiple WebDAV providers (round-robin):
```json
{
  "listen_addr": "127.0.0.1:1080",
  "storage_type": "webdav",
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
  },
  "enc_key": "your-aes-256-key-base64==",
  "hmac_key": "your-hmac-sha256-key-base64==",
  "socks5_user": "admin",
  "socks5_pass": "secret",
  "health_port": "127.0.0.1:9090"
}
```

Generate encryption keys:
```bash
head -c 32 /dev/urandom | base64 -w0; echo  # For enc_key
head -c 32 /dev/urandom | base64 -w0; echo  # For hmac_key
```

### Run with Docker Compose (testing)

First generate test configs with fresh encryption keys:

```bash
./scripts/prepare_test_env.sh
```

Then start all services:

```bash
# Build image and start all services
make docker-build
podman-compose up -d
```

Test the SOCKS5 proxy:

```bash
# From a container inside the Docker network:
podman run --rm --network flowdav_flow-net alpine:latest sh -c \
  "apk add --no-cache curl && curl -s --proxy socks5h://flow-client:11080 https://api.ipify.org"

# Or from the host (port 11080 is mapped to host):
curl -s --proxy socks5h://127.0.0.1:11080 https://api.ipify.org
```

**Port note:** The default SOCKS5 port is 1080 (localhost only). In docker-compose, the client exposes port 11080 on `0.0.0.0` to avoid conflicts with local services and allow host access.

### Manual Run

Generate example configs with fresh encryption keys:

```bash
# Only needed once — creates configs/client.json and configs/server.json
cp configs/flowdav_client.json.example configs/client.json
cp configs/flowdav_server.json.example configs/server.json
# Generate real keys (replace the placeholder values in the configs):
head -c 32 /dev/urandom | base64 -w0; echo   # → paste as enc_key
head -c 32 /dev/urandom | base64 -w0; echo   # → paste as hmac_key
```

**At home (server):**
```bash
./bin/flowdav-server -c configs/server.json
```

**At cafe (client):**
```bash
./bin/flowdav-client -c configs/client.json

# Use proxy in browser/app
export ALL_PROXY=socks5://127.0.0.1:1080
```

**Quick key generation (for docker-compose testing only):**
```bash
# Generates all test configs with fresh keys in configs/*.json
./scripts/prepare_test_env.sh
```

## Config Files

| File | Type | listen_addr | Health Port | Storage |
|------|------|-------------|-------------|---------|
| `flowdav_client.json.example` | Client | `127.0.0.1:1080` | `127.0.0.1:9090` | WebDAV |
| `flowdav_server.json.example` | Server | No | `127.0.0.1:9090` | WebDAV |

**Rule:** Client has `listen_addr` (SOCKS5 port), server does NOT (only polls WebDAV).

## Health Check

Both the client and server support an optional HTTP health endpoint. Set `health_port` in the config to enable it (e.g., `"127.0.0.1:9090"`). The endpoint `GET /health` returns JSON with engine statistics (active sessions, processed files, role, poll/flush rates).

In docker-compose, `HEALTHCHECK` is configured for both `flow-server` (port 9090) and `flow-client` (port 9091), enabling `depends_on` conditions and container orchestration health awareness.

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

# 2. Build Docker image and start services
podman-compose up -d --build

# 3. Run E2E test suite
./scripts/test_e2e.sh
```

### Release Archives

Multi-platform release archives are built with:

```bash
make release
```

Each archive contains: `flowdav-client`, `flowdav-server`, example configs, and README.

## License

MIT — see [LICENSE](./LICENSE) for details.

Flowdav is an independent implementation inspired by the concept of [NullLatency/FlowDriver](https://github.com/NullLatency/FlowDriver).
The original project does not specify a license; flowdav is released under its own terms.
