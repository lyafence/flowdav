# Contributing

## Building from source

```bash
make build
# Binary is in ./bin/flowdav
```

## Development workflow

```bash
make bench      # run benchmarks (performance regression check)
make check      # full verification: vet → lint → build → test with race detector
make test-e2e   # E2E tests (requires podman)
```

## Copy example config (from source checkout)

```bash
cp configs/flowdav.json.example config.json
```

## Docker Compose (E2E testing)

Quick full-stack test with three WebDAV backends and multi-client support:

```bash
./scripts/prepare_test_env.sh
make docker-build
./scripts/test_e2e.sh

# Or all at once:
make docker-e2e
```

Test the proxy after compose starts:

```bash
# Single-backend proxy
curl -s --proxy socks5h://127.0.0.1:11080 https://www.google.com

# Multi-backend proxy
curl -s --proxy socks5h://127.0.0.1:11081 https://www.google.com
```

> **Port note:** The default SOCKS5 port is 1080 (localhost only). Docker Compose exposes the single-backend proxy as `11080` and multi-backend as `11081` on `0.0.0.0` to avoid conflicts and allow host access.

In docker-compose, `HEALTHCHECK` is configured for all four services (`flowdav-server`, `flowdav-client`, `flowdav-server-multi`, `flowdav-client-multi`) on port `9190`.

## Android

```bash
make android-apk     # → bin/flowdav-android.apk
adb install -r bin/flowdav-android.apk
```

Requires: Android SDK + NDK + gomobile. Run `make android-init` to set up.

## Pull requests

1. Read the relevant package first — understand existing patterns.
2. Run `make check` after any change — full verification (vet → lint → build → test). For performance-sensitive changes, also run `make bench`.
3. If adding features to the SOCKS5/engine path, run `make test-e2e` or `make docker-e2e`.
4. Open a PR on [GitHub](https://github.com/lyafence/flowdav).

## Code style

- Go 1.26.5, stdlib preferred.
- Table-driven tests with `-race` enabled.
- Imports: stdlib → third-party → internal, blank-line separated.
- camelCase, no getters, unexported by default.
- Errors always checked and wrapped.
- No global state. Explicit `sync.Mutex`/`Map`/`Once`.
