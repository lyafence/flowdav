# flowdav — Agent Instructions

## Commands

| Command | What |
|---------|------|
| `make build` | Build unified binary → `./bin/flowdav` |
| `make check` | Full verification: vet → lint → build → test (with race detector) |
| `make test` | Unit tests with race detector (`-timeout 120s`) |
| `make test-short` | Unit tests without race detector |
| `make bench` | Run benchmarks: `go test -bench=. -benchmem -timeout 180s ./internal/transport/` |
| `make test-e2e` | E2E tests (requires podman; runs `scripts/test_e2e.sh`) |
| `make test-e2e-encrypted` | E2E + encrypted configs |
| `make fuzz` | Run fuzz tests (30s each: envelope + encrypted decoder) |
| `make vulncheck` | Run govulncheck vulnerability scan |
| `make encrypt FILE=config.json` | Encrypt config (also: `FLOWDAV_PASSWORD=secret make encrypt`) |
| `make tidy` | `go mod tidy -e` (CI checks `git diff go.mod go.sum`) |
| `make nuke` | Full env reset (compose down, clean images, build artifacts) |
| `make android-apk` | Build Android APK (debug) → `bin/flowdav-android.apk` |
| `make compose-android` | Build image and start the single-backend Android test env |
| `make android-deploy` | Build APK and deploy to an adb device. **Requires `make compose-android` first.** |
| `make openwrt` | Build mipsle binary for OpenWrt routers → `bin/flowdav-mipsle` |
| Additional (dev/CI) | `vet`, `lint`, `lint-kotlin`, `docker-build`, `image-to-bin`, `docker-e2e`, `compose-down`, `release`, `clean`, `clean-images`, `android-aar`, `android-init`, `android-keystore`, `ndk-version` |

> **Container tool:** Makefile targets use `podman` (`docker-build`, `image-to-bin`, `compose-*`). Do not assume `docker`.

> **Android test env:** `make compose-android` generates `configs/.env`; run it before `make android-deploy`. With multiple adb devices set `ANDROID_SERIAL=...`.

## Package Map

| Package | Responsibility |
|---------|---------------|
| `cmd/flowdav` | Entrypoints — unified binary |
| `cmd/android` | Gomobile bridge (exported to Android) |
| `internal/config` | Load, validate, encrypt/decrypt configs |
| `internal/transport` | Engine (poll loop, sessions), Envelope, Crypto, VirtualConn (SOCKS5), Pool. `ApplyConfigToEngine` imports `config` (leaf, no cycle); shared config-application lives here |
| `internal/storage` | WebDAV backend + MultiBackend (circuit breaker, round-robin, 429-aware cooldown) |
| `internal/logger` | Leveled logging |

## Architecture

```
SOCKS5 ←→ client ←→ WebDAV ←→ server ←→ destination
```

**Binary:** `flowdav` — unified entrypoint with `-c` (client), `-s` (server), `-e` (encrypt), `-g` (generate config) modes.  
**Android bridge:** `cmd/android/bridge.go` — gomobile bind, exports `StartProxyFromData`/`StartProxyManual`/`StopProxy`/`GetStatus`/`StopAndError`/`SetSocks5Auth`/`IsEncryptedConfig`/`TestStorage`/`ProbeTimeoutMs`/`Version` to Kotlin. **Global state intentional** (gomobile exports free functions, not objects). **Config validation delegates** to `config.ValidateAppConfig` — the two paths share the same validation logic. See `parseConfigJSON` in bridge.go. **`GetStatus` violates "no getters" intentionally** — Java/Kotlin interop dictates Java Beans naming. **Errors cross the bridge as stable codes**: `StartProxy*` returns `*StartResult{Success, Code, Message}`, `Status.ErrorCode`/`StopAndError` carry codes; Android maps them via `ProxyState.ErrorKind`. The contract is pinned by `TestErrorCodesStable` (Go) and `ErrorKindCodesTest` (Kotlin, parses bridge.go) — keep both sides in sync.

## Design Invariants

- Server has **zero listening ports** for data — all data via WebDAV storage (optional health endpoint on loopback only).
- WebDAV is passive dumb storage; client encrypts/muxes, server decrypts/demuxes.
- AES-256-GCM + HMAC-SHA256 on all data.
- Random filenames `{dir_byte}{16_hex}` — no metadata leaks. Mapped to `{subdir}/{uppercase_hex}` on storage (direction byte → subdirectory: `r`→`invoices`, `s`→`receipts`).
- WebDAV listings accept only payload names (`16` hex chars) and skip directories. Do not widen this to arbitrary files: `processDownload` deletes files it consumes, so a shared WebDAV directory can lose unrelated data.
- Backend operations must honor `ctx`: `Upload`/`Delete` use `http.NewRequestWithContext`; `Login`/`ListQuery` use the cancellable wrapper. Do not reintroduce `_ = ctx` or unbounded shutdown.
- Config parsing is strict: unknown/typo JSON fields fail for `-c`, `-s`, `-e`, and Android import. `-e` validates with `config.ParseAppConfig` before writing `.enc`; do not bypass it with a raw `json.Unmarshal` into a map.
- DNS leak protection: raw resolver, UDP explicitly blocked.
- Multi-WebDAV: random session assignment, round-robin upload fallback.
- **One keypair = one client** (deliberate single-client scope — not a bug). `Envelope` carries no client identity, and in `internal/transport/pool.go` `processDownload` a client **deletes every response file it downloads**, even one whose `SessionID` it does not know (only the server — `DirRes` — auto-creates sessions on unknown IDs; `internal/transport/engine.go` pollLoop skips polling when the client has zero sessions). Two clients sharing `enc_key`/`hmac_key` + `base_path` therefore steal and destroy each other's packets. Multi-device = one client/server pair per device, each with a distinct keypair and `base_path`. Kept intentional — do not add client routing without a design pass.
- **429-aware circuit breaker**: rate-limit (429) uses separate 60s cooldown without incrementing failure count. Session auto-migrates to another backend on 429. Download falls back to non-indexed search across all backends.
- **TLS fingerprint masking**: uTLS `HelloChrome_133` by default (configurable via `tls_fingerprint`). User-Agent matches (`Chrome/133.0.0.0`).
- No mutable global state (exceptions: `transport.MaxMessageSize` / `storage.MaxFileSize` — package-level vars set at startup, justified by OOM prevention per `internal/transport/crypto.go` `MaxMessageSize` var; `internal/config` `pbkdf2Iter` — test-only PBKDF2 cost knob; `cmd/*` `version` — build-time value injected via ldflags; `internal/logger` — package-level `level` var for leveled logging is standard and excluded by convention; `cmd/android/bridge.go` — gomobile requires free functions, not objects, so global state is unavoidable and documented as intentional).
- **Operational philosophy:** minimize external observability, avoid predictable patterns. When adding anything network-facing: is it optional? bounded? indistinguishable from noise?

## Pre-commit Hook

The pre-commit hook (`.githooks/pre-commit`) checks:
- `gofmt` on `cmd/ internal/`
- `goimports` with `-local github.com/lyafence/flowdav`
- `go vet ./...`
- Bans `math/rand`, `os/exec` in production code (`cmd/`, `internal/`, excludes `_test.go`)
- Bans `database/sql` in production code (same scope)
- Bans `sync.Pool` in production code (same scope)
- `gitleaks` on staged changes (optional; skipped if not installed)

Install with `make hooks`.

## CI / GitHub Actions

- Actions must be pinned to a full-length commit SHA (`uses: owner/repo@<40-char-sha> # vX.Y.Z`) — enforced repo-wide (`sha_pinning_required`), incl. `actions/*`.
- Prefer first-party `actions/*` and runner-preinstalled tooling over third-party actions.
- Dependabot updates `uses:` refs, not `run:` tool versions — pin only where it matters.
- Kotlin style gate: `make lint-kotlin` (ktlint, version pinned in the Makefile; `android_studio` style via `.editorconfig`).
- CI's fuzz list must mirror `make fuzz`; when adding a fuzz target, update `.github/workflows/ci.yml` too (use anchored `-fuzz '^Name$'`).

## Config Quick Reference

- Flags: `-c config.json` (client), `-s config.json` (server), `-e config.json` (encrypt), `-g config.json` (generate), `-p master_password`, `-l loglevel`, `--version`, `--qr` (QR of encrypted config with `-e`), `--remove-source` (delete plaintext after `-e`)
- Fields: `enc_key` / `hmac_key` (32-byte base64), `max_message_size` (default 16MB), `max_sessions` (server, default 0 = unlimited), `idle_timeout_ms` (default 10000), `webdav.backends` (array), `health_port` (e.g. `"127.0.0.1:9191"`), `log_level` (`debug`, `info`, `warn`, `error`)
- `padding_size` (bucket size for tail padding; 0 = off), `hold_ms` (max server-side random delay before flush; 0 = off) — optional settings
- `socks5_user` / `socks5_pass` must be set together; a partial pair is rejected by `ValidateAppConfig` (it would silently select NoAuth)
- Health: `GET /health` on `health_port` → JSON stats (active sessions, retry counters, tx queue depth, per-backend circuit breaker state)

## Documentation Audiences

| Document | Ships in archive | Audience | Constraint |
|----------|-----------------|----------|------------|
| `README.md` | Yes | End user | All commands must work from release tarball. **Never reference `make`, `go run`, or source tree paths.** |
| `AGENTS.md` | No | Agent | Can reference `make`, scripts, Go toolchain. |
| `CONTRIBUTING.md` | No | Developer / Agent | Development workflow, code style, PR guide. AGENTS.md is authoritative for agents. |

## Coding Conventions

- Go 1.27.3, stdlib preferred.
- Tests: `testing` package, table-driven, `-race` enabled.
- Benchmarks: `bench_test.go` in `internal/transport`. Run with `make bench`.
- Imports: stdlib → third-party → internal, blank-line separated.
- Naming: camelCase, no getters, unexported by default.
- Errors: always checked and wrapped. Log runtime errors via `internal/logger`; CLI startup failures may use `log.Fatalf`.
- Thread safety: explicit `sync.Mutex`/`Map`/`Once`. Document lock order.

## Agent Constraints

- **NEVER commit or push** without explicit command.
- Unauthorized commit → user says `revert` → revert immediately.
- Destructive git operations (revert, reset, force push, rebase): **ask first**.

## Agent Workflow

1. Read the relevant package before writing code.
2. `make check` after any change — full verification (vet → lint → build → test with race detector).
3. SOCKS5/engine changes → `make test-e2e` or `make docker-e2e` (requires podman).
4. Encrypted config changes → `make test-e2e-encrypted`.
5. After adding/removing dependencies → `go mod tidy` (CI checks `git diff go.mod go.sum`).

## Anti-Patterns

- **Coverage Theater** — don't write tests you can't name a real bug for.
- **Refactoring for Testability** — test through public API, not extracted private methods.
- **Don't clean what you don't understand** — double-select shutdown, redundant Store guards — often intentional.
- **No cargo cult** — understand why a pattern exists before copying it. Just because it's in the codebase doesn't mean it belongs in your change.
- **YAGNI** — don't add features "just in case". If you don't need it today, you probably won't need it tomorrow. Adding it later is cheaper than maintaining it now.
- **Memory buffering** — txBuf per session is deliberate (2MB cap, blocks on backpressure instead of extra HTTP calls). `sync.Pool` for txBuf is premature without profiling.
- **ACK/retransmission** — control packets add predictable patterns. Best-effort is deliberate.
- **Persistent state / SQLite** — local artifacts and disk writes, no need.
- **Verbose logging / event tracing** — if ever needed: local-only, off by default.
- **Remote metrics (Prometheus, etc.)** — if ever needed: local-only, off by default.
- **Config flag creep** — not every internal constant needs a user-facing flag. Defaults are meant to be defaults.
