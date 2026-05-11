#!/bin/bash
# Prepare test configs for flowdav E2E tests.
#
# Usage:
#   ./scripts/prepare_test_env.sh                          # plaintext JSON
#   ./scripts/prepare_test_env.sh --encrypted               # encrypted .enc
#   ./scripts/prepare_test_env.sh --encrypted --password X
#
# Generates configs in configs/:
#   flowdav_test_{client,server}{,_multi}.json
#
# With --encrypted: overwrites .json with encrypted content and
# writes .env file with ENC_PASS for docker-compose to pick up.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
CONFIG_DIR="$PROJECT_DIR/configs"
ENCRYPTED=false
ENC_PASS=""

while [[ $# -gt 0 ]]; do
    case $1 in
        --encrypted) ENCRYPTED=true ;;
        --password)  ENC_PASS="$2"; shift ;;
        *) echo "Unknown: $1"; exit 1 ;;
    esac
    shift
done

if $ENCRYPTED && [ -z "$ENC_PASS" ]; then
    ENC_PASS="test-master-password-123"
fi

enc_key=$(head -c 32 /dev/urandom | base64 -w0)
hmac_key=$(head -c 32 /dev/urandom | base64 -w0)
enc_key_multi=$(head -c 32 /dev/urandom | base64 -w0)
hmac_key_multi=$(head -c 32 /dev/urandom | base64 -w0)

gen_config() {
    local file="$CONFIG_DIR/$1"
    local json="$2"
    if $ENCRYPTED; then
        FLOWDAV_PASSWORD="$ENC_PASS" go run "$PROJECT_DIR/cmd/encrypt/main.go" > "$file" <<<"$json"
    else
        echo "$json" > "$file"
    fi
}

gen_config "flowdav_test_client.json" '{
  "listen_addr": "0.0.0.0:11080",
  "storage_type": "webdav",
  "webdav": {
    "provider": "custom",
    "url": "http://webdav-test:8080",
    "login": "test",
    "token": "test",
    "base_path": "myapp"
  },
  "refresh_rate_ms": 500,
  "flush_rate_ms": 500,
  "enc_key": "'"$enc_key"'",
  "hmac_key": "'"$hmac_key"'",
  "log_level": "debug",
  "socks5_user": "",
  "socks5_pass": "",
  "health_port": "127.0.0.1:9191"
}'

gen_config "flowdav_test_server.json" '{
  "storage_type": "webdav",
  "webdav": {
    "provider": "custom",
    "url": "http://webdav-test:8080",
    "login": "test",
    "token": "test",
    "base_path": "myapp"
  },
  "enc_key": "'"$enc_key"'",
  "hmac_key": "'"$hmac_key"'",
  "refresh_rate_ms": 500,
  "flush_rate_ms": 500,
  "log_level": "debug",
  "health_port": "127.0.0.1:9190"
}'

gen_config "flowdav_test_client_multi.json" '{
  "listen_addr": "0.0.0.0:11080",
  "storage_type": "webdav",
  "webdav": {
    "backends": [
      {
        "provider": "custom",
        "url": "http://webdav-test:8080",
        "login": "test",
        "token": "test",
        "base_path": "app1"
      },
      {
        "provider": "custom",
        "url": "http://webdav-test-2:8080",
        "login": "test",
        "token": "test",
        "base_path": "app2"
      },
      {
        "provider": "custom",
        "url": "http://webdav-test-3:8080",
        "login": "test",
        "token": "test",
        "base_path": "app3"
      }
    ]
  },
  "refresh_rate_ms": 500,
  "flush_rate_ms": 500,
  "enc_key": "'"$enc_key_multi"'",
  "hmac_key": "'"$hmac_key_multi"'",
  "log_level": "debug",
  "socks5_user": "",
  "socks5_pass": "",
  "health_port": "127.0.0.1:9191"
}'

gen_config "flowdav_test_server_multi.json" '{
  "storage_type": "webdav",
  "webdav": {
    "backends": [
      {
        "provider": "custom",
        "url": "http://webdav-test:8080",
        "login": "test",
        "token": "test",
        "base_path": "app1"
      },
      {
        "provider": "custom",
        "url": "http://webdav-test-2:8080",
        "login": "test",
        "token": "test",
        "base_path": "app2"
      },
      {
        "provider": "custom",
        "url": "http://webdav-test-3:8080",
        "login": "test",
        "token": "test",
        "base_path": "app3"
      }
    ]
  },
  "refresh_rate_ms": 500,
  "flush_rate_ms": 500,
  "enc_key": "'"$enc_key_multi"'",
  "hmac_key": "'"$hmac_key_multi"'",
  "log_level": "debug",
  "health_port": "127.0.0.1:9190"
}'

echo "Generated test configs in $CONFIG_DIR:"
for f in flowdav_test_client flowdav_test_server flowdav_test_client_multi flowdav_test_server_multi; do
    echo "  $f.json"
done

if $ENCRYPTED; then
    cat > "$CONFIG_DIR/.env" <<EOF
FLOWDAV_PASSWORD=${ENC_PASS}
EOF
    echo "  .env (with FLOWDAV_PASSWORD)"
fi
