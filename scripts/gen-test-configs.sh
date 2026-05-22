#!/bin/sh
# Generate test config for compose-android (server + client).
# Usage: scripts/gen-test-configs.sh HOST_IP
set -eu

HOST_IP="${1:?Usage: gen-test-configs.sh HOST_IP}"
ENC_KEY=$(head -c 32 /dev/urandom | base64 -w0)
HMAC_KEY=$(head -c 32 /dev/urandom | base64 -w0)

cat > configs/flowdav_test.json <<END
{
  "storage_type": "webdav",
  "webdav": {
    "url": "http://${HOST_IP}:8080",
    "login": "test",
    "token": "test",
    "base_path": "data_sync"
  },
  "listen_addr": "0.0.0.0:1080",
  "enc_key": "${ENC_KEY}",
  "hmac_key": "${HMAC_KEY}",
  "refresh_rate_ms": 500,
  "flush_rate_ms": 500,
  "log_level": "debug",
  "health_port": "127.0.0.1:9190"
}
END
