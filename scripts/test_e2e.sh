#!/bin/bash
# flowdav End-to-End Test Suite
#
# Usage:
#   ./scripts/test_e2e.sh                         # plaintext configs (default)
#   ./scripts/test_e2e.sh --encrypted              # encrypted configs
#   ./scripts/test_e2e.sh --encrypted --build      # build image + encrypted
#   ./scripts/test_e2e.sh --build                  # build image + plaintext
#   ./scripts/test_e2e.sh --skip-compose           # unit tests only
#
# Requirements: podman (or docker), podman-compose (or docker compose)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
TIMEOUT=20
TESTS_PASSED=0
TESTS_FAILED=0
BUILD=false
ENCRYPTED=false
SKIP_COMPOSE=false
COMPOSE=""

log_pass() { echo "  PASS: $1"; TESTS_PASSED=$((TESTS_PASSED + 1)); }
log_fail() { echo "  FAIL: $1"; TESTS_FAILED=$((TESTS_FAILED + 1)); }
log_info() { echo "     $1"; }

wait_for_health() {
    local name="$1" container="$2" check_cmd="$3" retries="${4:-10}" interval="${5:-3}"
    log_info "Waiting for $name..."
    for i in $(seq 1 "$retries"); do
        if $COMPOSE exec "$container" sh -c "$check_cmd" >/dev/null 2>&1; then
            log_pass "$name is healthy"
            return 0
        fi
        sleep "$interval"
    done
    log_fail "$name not healthy after $((retries * interval))s"
    return 1
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --build) BUILD=true ;;
        --encrypted) ENCRYPTED=true ;;
        --skip-compose) SKIP_COMPOSE=true ;;
        *) echo "Unknown: $1"; exit 1 ;;
    esac
    shift
done

# Detect compose tool
if command -v podman-compose &>/dev/null; then
    COMPOSE="podman-compose"
elif command -v docker-compose &>/dev/null; then
    COMPOSE="docker-compose"
elif docker compose version &>/dev/null 2>&1; then
    COMPOSE="docker compose"
fi

echo "=========================================="
echo "  flowdav E2E Test Suite"
echo "  Mode: $($ENCRYPTED && echo 'encrypted' || echo 'plaintext')"
echo "  Build: $($BUILD && echo 'yes' || echo 'no')"
echo "  Compose: $($SKIP_COMPOSE && echo 'skipped' || echo "$COMPOSE")"
echo "=========================================="

# ── Phase 1: Unit tests ──────────────────────
echo ""
echo "--- Phase 1: Unit & Integration Tests ---"
log_info "Running go test (race enabled)..."

if ! go test -race -count=1 -timeout 120s ./... 2>&1; then
    log_fail "Unit tests failed"
else
    log_pass "All unit tests pass"
fi

# ── Phase 2: Encrypted config E2E ────────────
if $ENCRYPTED; then
    echo ""
    echo "--- Phase 2: Encrypted Config E2E ---"
    log_info "Running TestEncryptedConfigEndToEnd..."
    if ! go test -race -count=1 -timeout 90s \
        -run TestEncryptedConfigEndToEnd ./internal/config/ -v 2>&1; then
        log_fail "Encrypted config E2E failed"
    else
        log_pass "Encrypted config E2E passed"
    fi
fi

if $SKIP_COMPOSE || [ -z "$COMPOSE" ]; then
    # Summary for skip-compose mode
    echo ""
    echo "=========================================="
    echo "  TESTS PASSED: $TESTS_PASSED"
    echo "  TESTS FAILED: $TESTS_FAILED"
    echo "=========================================="
    [ "$TESTS_FAILED" -eq 0 ]
    exit $?
fi

# ── Phase 3: Generate test configs ──────────
echo ""
echo "--- Phase 3: Generate Test Configs ---"
ENCRYPTED_FLAG=""
$ENCRYPTED && ENCRYPTED_FLAG="--encrypted"
bash "$SCRIPT_DIR/prepare_test_env.sh" $ENCRYPTED_FLAG
echo ""

# ── Phase 4: Build image (optional) ────────
if $BUILD; then
    echo "--- Phase 4: Build Container Image ---"
    podman build -t localhost/flowdav:latest -f "$PROJECT_DIR/Dockerfile" "$PROJECT_DIR"
    echo ""
else
    echo "--- Phase 4: Build Image (skipped) ---"
    log_info "Use --build to rebuild the container image"
    echo ""
fi

# ── Phase 5: Start services ────────────────
echo "--- Phase 5: Start Services ---"
# -v removes anonymous volumes (rclone /data writable layer), preventing
# stale WebDAV files with mismatched encryption keys from polluting a new run.
$COMPOSE -f "$PROJECT_DIR/docker-compose.yml" down -v 2>/dev/null || true
if $ENCRYPTED; then
    export FLOWDAV_PASSWORD=$(grep FLOWDAV_PASSWORD "$PROJECT_DIR/configs/.env" | cut -d= -f2)
    $COMPOSE -f "$PROJECT_DIR/docker-compose.yml" up -d 2>&1
else
    $COMPOSE -f "$PROJECT_DIR/docker-compose.yml" up -d 2>&1
fi

log_info "Waiting for services to become healthy..."
# Parallel health checks — each waits independently with retry
for svc in "webdav-multi-1|wget -q --spider http://test:test@localhost:8080/" \
           "webdav-single|wget -q --spider http://test:test@localhost:8080/" \
           "flowdav-server|curl -sf http://127.0.0.1:9190/health" \
           "flowdav-client|curl -sf http://127.0.0.1:9190/health" \
           "flowdav-server-multi|curl -sf http://127.0.0.1:9190/health" \
           "flowdav-client-multi|curl -sf http://127.0.0.1:9190/health"; do
    name="${svc%%|*}"
    cmd="${svc#*|}"
    case "$name" in
        webdav*) wait_for_health "$name" "$name" "$cmd" 15 2 || true &
                 ;;
        *)       wait_for_health "$name" "$name" "$cmd" 10 3 || true &
                 ;;
    esac
done
wait
echo ""

# ── Phase 6: SOCKS5 Proxy Tests ────────────
echo "--- Phase 6: Proxy Connectivity Tests ---"
log_info "Waiting for SOCKS5 proxy to accept connections..."
for i in 1 2 3; do
    if curl -sf --proxy socks5h://127.0.0.1:11080 http://example.com --max-time 2 >/dev/null 2>&1; then
        log_pass "SOCKS5 proxy is ready"
        break
    fi
    sleep 1
done

log_info "Waiting for multi-backend SOCKS5 proxy..."
for i in 1 2 3; do
    if curl -sf --proxy socks5h://127.0.0.1:11081 http://example.com --max-time 2 >/dev/null 2>&1; then
        log_pass "Multi-backend SOCKS5 proxy is ready"
        break
    fi
    sleep 1
done

# Helper: safe_curl wraps curl with || true so set -e does not abort
# the script on transient proxy delays. Test failures are tracked by log_fail.
safe_curl() {
    curl -s "$@" --max-time "$TIMEOUT" 2>&1 || true
}

# Test 1: Basic SOCKS5 (with retry for cold proxy)
log_info "Test 1: Basic SOCKS5 proxy..."
for i in 1 2 3; do
    RESULT=$(safe_curl --proxy socks5h://127.0.0.1:11080 https://api.ipify.org)
    if [ -n "$RESULT" ] && [ ${#RESULT} -lt 20 ] && [[ "$RESULT" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        log_pass "Test 1: Got IP = $RESULT"
        break
    fi
    if [ "$i" -lt 3 ]; then
        log_info "  Retry $i/3 in $((2**(i-1)))s..."
        sleep "$((2**(i-1)))"
    else
        log_fail "Test 1: No valid IP (got: ${RESULT:0:100})"
    fi
done

# Test 2: HTTPS site
log_info "Test 2: HTTPS (google.com)..."
HTTP_CODE=$(safe_curl -o /dev/null -w "%{http_code}" --proxy socks5h://127.0.0.1:11080 \
    "https://www.google.com" --max-redirs 0)
if [ "$HTTP_CODE" = "200" ]; then
    log_pass "Test 2: google.com 200"
else
    log_fail "Test 2: google.com $HTTP_CODE"
fi

# Test 3: HTTP site
log_info "Test 3: HTTP (example.com)..."
RESULT=$(safe_curl --proxy socks5h://127.0.0.1:11080 "http://example.com" | head -c 50)
if [ -n "$RESULT" ] && [ ${#RESULT} -gt 10 ]; then
    log_pass "Test 3: HTTP via proxy (${#RESULT} bytes)"
else
    log_fail "Test 3: HTTP via proxy failed"
fi

# Test 4: Direct IP (DNS leak check)
log_info "Test 4: Direct IP..."
RESULT=$(safe_curl --proxy socks5h://127.0.0.1:11080 "http://216.239.38.120/" | head -c 50)
if [ -n "$RESULT" ] && [ ${#RESULT} -gt 10 ]; then
    log_pass "Test 4: Direct IP connection works"
else
    log_fail "Test 4: Direct IP connection failed"
fi

# Test 5: Multi-backend proxy (with retry for cold proxy)
log_info "Test 5: Multi-backend proxy..."
for i in 1 2 3; do
    RESULT=$(safe_curl --proxy socks5h://127.0.0.1:11081 "http://example.com" | head -c 50)
    if [ -n "$RESULT" ] && [ ${#RESULT} -gt 10 ]; then
        log_pass "Test 5: Multi-backend proxy works (${#RESULT} bytes)"
        break
    fi
    if [ "$i" -lt 3 ]; then
        log_info "  Retry $i/3 in $((2**(i-1)))s..."
        sleep "$((2**(i-1)))"
    else
        log_fail "Test 5: Multi-backend failed (got: ${RESULT:0:50})"
    fi
done

# ── Cleanup ─────────────────────────────────
echo ""
echo "--- Cleanup ---"
log_info "Stopping services..."
$COMPOSE -f "$PROJECT_DIR/docker-compose.yml" down -v 2>&1 || true
log_info "Cleaning up test configs..."
rm -f "$PROJECT_DIR"/configs/flowdav_test*.json "$PROJECT_DIR"/configs/.env 2>/dev/null || true
echo ""

# ── Summary ─────────────────────────────────
echo "=========================================="
echo "  PASSED: $TESTS_PASSED"
echo "  FAILED: $TESTS_FAILED"
echo "=========================================="

if [ "$TESTS_FAILED" -eq 0 ]; then
    echo "  ALL TESTS PASSED!"
    exit 0
else
    echo "  SOME TESTS FAILED!"
    exit 1
fi
