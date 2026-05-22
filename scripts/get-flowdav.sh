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

url="https://github.com/lyafence/flowdav/releases/latest/download/flowdav-latest-${os}-${arch}.tar.gz"

echo "Downloading flowdav for ${os}-${arch}..."
curl -sSfL "$url" | tar -xz --strip-components=1
chmod +x flowdav
echo "flowdav ready. Run ./flowdav --help to get started."
