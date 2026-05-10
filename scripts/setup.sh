#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CONFIG_DIR="$SCRIPT_DIR/../configs"

enc_key=$(openssl rand -base64 32)
hmac_key=$(openssl rand -base64 32)
enc_key_multi=$(openssl rand -base64 32)
hmac_key_multi=$(openssl rand -base64 32)

gen_client() {
  cat > "$CONFIG_DIR/flowdav_test_client.json" <<EOF
{
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
  "enc_key": "${enc_key}",
  "hmac_key": "${hmac_key}",
  "log_level": "debug",
  "socks5_user": "",
  "socks5_pass": "",
  "health_port": "127.0.0.1:9091"
}
EOF
}

gen_server() {
  cat > "$CONFIG_DIR/flowdav_test_server.json" <<EOF
{
  "storage_type": "webdav",
  "webdav": {
    "provider": "custom",
    "url": "http://webdav-test:8080",
    "login": "test",
    "token": "test",
    "base_path": "myapp"
  },
  "enc_key": "${enc_key}",
  "hmac_key": "${hmac_key}",
  "refresh_rate_ms": 500,
  "flush_rate_ms": 500,
  "log_level": "debug",
  "health_port": "127.0.0.1:9090"
}
EOF
}

gen_client_multi() {
  cat > "$CONFIG_DIR/flowdav_test_client_multi.json" <<EOF
{
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
  "enc_key": "${enc_key_multi}",
  "hmac_key": "${hmac_key_multi}",
  "log_level": "debug",
  "socks5_user": "",
  "socks5_pass": "",
  "health_port": "127.0.0.1:9091"
}
EOF
}

gen_server_multi() {
  cat > "$CONFIG_DIR/flowdav_test_server_multi.json" <<EOF
{
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
  "enc_key": "${enc_key_multi}",
  "hmac_key": "${hmac_key_multi}",
  "refresh_rate_ms": 500,
  "flush_rate_ms": 500,
  "log_level": "debug",
  "health_port": "127.0.0.1:9090"
}
EOF
}

gen_client
gen_server
gen_client_multi
gen_server_multi

echo "Generated test configs in $CONFIG_DIR:"
echo "  flowdav_test_client.json"
echo "  flowdav_test_server.json"
echo "  flowdav_test_client_multi.json"
echo "  flowdav_test_server_multi.json"
