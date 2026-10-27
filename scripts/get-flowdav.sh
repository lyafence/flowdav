#!/bin/sh
# flowdav install script — auto-detect OS and architecture, download from GitHub Releases.
# Usage: curl -sSf https://raw.githubusercontent.com/lyafence/flowdav/main/scripts/get-flowdav.sh | sh
set -eu

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
	linux) ;;
	darwin) ;;
	*)
		echo "Unsupported OS: $os"
		echo "Download manually from https://github.com/lyafence/flowdav/releases"
		exit 1
		;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64|amd64) arch="amd64" ;;
	arm64|aarch64) arch="arm64" ;;
	*)
		echo "Unsupported architecture: $arch"
		echo "Download manually from https://github.com/lyafence/flowdav/releases"
		exit 1
		;;
esac

base="https://github.com/lyafence/flowdav/releases/latest/download"
asset="flowdav-latest-${os}-${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading flowdav for ${os}-${arch}..."
curl -sSfL "$base/$asset" -o "$tmp/$asset"
# Note: SHA256SUMS.txt comes from the same release — the check is TOFU
# (trust-on-first-use on GitHub); it protects against transport errors, not
# against a fully compromised release.
if ! curl -sSfL "$base/SHA256SUMS.txt" -o "$tmp/SHA256SUMS.txt"; then
	echo "SHA256SUMS.txt is missing from the latest release — refusing to install unverified" >&2
	echo "(releases older than the checksum feature cannot be verified; download manually and check the hash if you must)" >&2
	exit 1
fi

# Verify integrity before extracting. The checksum file is fetched from the
# same release over TLS; it protects against corrupted/truncated downloads
# and in-transit tampering of the archive itself.
checksum_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

# Exact match: a substring lookup would match "flowdav" inside "flowdav-android.apk".
expected=$(awk -v a="$asset" '$2==a {print $1; exit}' "$tmp/SHA256SUMS.txt")
if [ -z "$expected" ]; then
	echo "No checksum found for $asset in SHA256SUMS.txt — aborting" >&2
	exit 1
fi
actual=$(checksum_of "$tmp/$asset")
if [ "$expected" != "$actual" ]; then
	echo "Checksum mismatch for $asset:" >&2
	echo "  expected: $expected" >&2
	echo "  actual:   $actual" >&2
	exit 1
fi
echo "Checksum verified."

tar -xzf "$tmp/$asset" --strip-components=1
chmod +x flowdav
echo "flowdav ready. Run ./flowdav --help to get started."
