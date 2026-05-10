#!/bin/bash
set -euo pipefail

SOCKS5_PROXY="socks5h://flow-client:11080"
SOCKS5_PROXY_MULTI="socks5h://flow-client-multi:11080"
TIMEOUT=60
TESTS_PASSED=0
TESTS_FAILED=0

log_pass() { echo "✅ PASS: $1"; ((TESTS_PASSED++)); }
log_fail() { echo "❌ FAIL: $1"; ((TESTS_FAILED++)); }
log_info()  { echo "ℹ️  $1"; }

echo "=========================================="
echo "flowdav Comprehensive Proxy Test Suite"
echo "=========================================="

# Test 1: Basic SOCKS5 connectivity
log_info "Test 1: Basic SOCKS5 proxy connectivity..."
RESULT=$(curl -s --proxy "$SOCKS5_PROXY" https://api.ipify.org --max-time $TIMEOUT 2>&1)
if [ -n "$RESULT" ] && [ ${#RESULT} -lt 20 ] && [[ "$RESULT" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    log_pass "Got IP = $RESULT"
else
    log_fail "No valid IP returned (got: ${RESULT:0:100})"
fi

# Test 2: HTTPS Google
log_info "Test 2: HTTPS sites (google.com)..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" --proxy "$SOCKS5_PROXY" "https://www.google.com" --max-time $TIMEOUT --max-redirs 0 2>&1)
if [ "$HTTP_CODE" = "200" ]; then
    log_pass "google.com returned 200"
else
    log_fail "google.com returned $HTTP_CODE (expected 200)"
fi

# Test 3: HTTPS GitHub
log_info "Test 3: HTTPS GitHub..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" --proxy "$SOCKS5_PROXY" "https://www.github.com" --max-time $TIMEOUT --max-redirs 0 2>&1)
if [ "$HTTP_CODE" = "200" ] || [ "$HTTP_CODE" = "301" ]; then
    log_pass "github.com returned $HTTP_CODE"
else
    log_fail "github.com returned $HTTP_CODE"
fi

# Test 4: HTTP (non-S)
log_info "Test 4: HTTP site via proxy..."
RESULT=$(curl -s --proxy "$SOCKS5_PROXY" "http://example.com" --max-time $TIMEOUT 2>&1 | head -c 50)
if [ -n "$RESULT" ] && [ ${#RESULT} -gt 10 ]; then
    log_pass "HTTP via proxy works (${#RESULT} bytes)"
else
    log_fail "HTTP via proxy failed"
fi

# Test 5: DNS leak check (direct IP)
log_info "Test 5: DNS leak protection (direct IP)..."
RESULT=$(curl -s --proxy "$SOCKS5_PROXY" "http://216.239.38.120/" --max-time $TIMEOUT 2>&1 | head -c 50)
if [ -n "$RESULT" ] && [ ${#RESULT} -gt 10 ]; then
    log_pass "Direct IP connection works"
else
    log_fail "Direct IP connection failed"
fi

# Test 6: Concurrent connections
log_info "Test 6: Testing concurrent connections..."
CONCURRENCY=5
for i in $(seq 1 $CONCURRENCY); do
    (curl -s --proxy "$SOCKS5_PROXY" https://api.ipify.org --max-time $TIMEOUT > /tmp/proxy_test_$i.txt 2>&1) &
done
wait

SUCCESS=0
for i in $(seq 1 $CONCURRENCY); do
    if [ -s /tmp/proxy_test_$i.txt ] && grep -qE '^[0-9]+\.' /tmp/proxy_test_$i.txt; then
        ((SUCCESS++))
    fi
    rm -f /tmp/proxy_test_$i.txt
done
if [ "$SUCCESS" -eq "$CONCURRENCY" ]; then
    log_pass "$SUCCESS/$CONCURRENCY concurrent connections successful"
else
    log_fail "$SUCCESS/$CONCURRENCY concurrent connections (expected all)"
fi

# Test 7: httpbin.org/get
log_info "Test 7: httpbin.org/get (JSON API)..."
RESULT=$(curl -s --proxy "$SOCKS5_PROXY" "https://httpbin.org/get" --max-time $TIMEOUT 2>&1)
if echo "$RESULT" | grep -q '"origin"'; then
    IP=$(echo "$RESULT" | grep -o '"origin"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1)
    log_pass "httpbin.org returned: $IP"
else
    log_fail "httpbin.org returned invalid response"
fi

# Test 8: Multi-backend proxy
log_info "Test 8: Multi-backend proxy (flow-client-multi)..."
RESULT=$(curl -s --proxy "$SOCKS5_PROXY_MULTI" https://api.ipify.org --max-time $TIMEOUT 2>&1)
if [ -n "$RESULT" ] && [[ "$RESULT" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    log_pass "Multi-backend got IP = $RESULT"
else
    log_fail "Multi-backend no valid IP (got: ${RESULT:0:50})"
fi

# Summary
echo ""
echo "=========================================="
echo "TEST SUMMARY"
echo "=========================================="
echo "Passed: $TESTS_PASSED"
echo "Failed: $TESTS_FAILED"
echo ""

if [ "$TESTS_FAILED" -eq 0 ]; then
    echo "✅ ALL TESTS PASSED!"
    exit 0
else
    echo "❌ SOME TESTS FAILED!"
    exit 1
fi