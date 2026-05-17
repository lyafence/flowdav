#!/bin/bash
# Generate Android release keystore and print GitHub Secrets.
# Usage: bash scripts/gen-keystore.sh
#
# Output: base64 + secrets — copy-paste into GitHub Settings → Secrets → Actions.
# Local .keystore file is deleted immediately after encoding.
#
# Prerequisites: keytool (from JDK), openssl, base64

set -euo pipefail

KP=$(openssl rand -base64 32 | tr -dc 'a-zA-Z0-9!@#$%^&*()_+-=' | head -40)

keytool -genkey -v -keystore flowdav-release.keystore \
  -alias flowdav -keyalg RSA -keysize 2048 -validity 10000 \
  -storepass "$KP" -keypass "$KP" \
  -dname "CN=flowdav, OU=, O=lyafence, L=, S=, C="

echo ""
echo "=== ANDROID_KEYSTORE_BASE64 ==="
base64 -w0 flowdav-release.keystore
echo ""
echo ""
echo "=== GitHub Secrets (copy below) ==="
echo "ANDROID_KEYSTORE_PASSWORD=$KP"
echo "ANDROID_KEY_ALIAS=flowdav"

rm flowdav-release.keystore
