#!/bin/bash
set -euo pipefail

BINARY_DIR="bin"
RELEASE_DIR="release"
BUILD_FLAGS=(-trimpath "-ldflags=-s -w")

mkdir -p "${BINARY_DIR}" "${RELEASE_DIR}"

build_binaries() {
    echo "Building linux binaries to ${BINARY_DIR}/..."
    CGO_ENABLED=0 GOOS=linux go build "${BUILD_FLAGS[@]}" -o "${BINARY_DIR}/flowdav-client" ./cmd/client || { echo "Failed to build flowdav-client"; exit 1; }
    CGO_ENABLED=0 GOOS=linux go build "${BUILD_FLAGS[@]}" -o "${BINARY_DIR}/flowdav-server" ./cmd/server || { echo "Failed to build flowdav-server"; exit 1; }
    echo "Done! Binaries in ${BINARY_DIR}/"
}

build_image() {
    echo "Building Docker image..."
    podman build -t localhost/flowdav:latest -f Dockerfile .
    echo "Done! Image: flowdav:latest"
}

build_release() {
    echo "Building release archives (multi-platform)..."

# Linux amd64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build "${BUILD_FLAGS[@]}" -o "${BINARY_DIR}/flowdav-client-linux-amd64" ./cmd/client
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build "${BUILD_FLAGS[@]}" -o "${BINARY_DIR}/flowdav-server-linux-amd64" ./cmd/server
zip -j "${RELEASE_DIR}/flowdav-linux-amd64.zip" "${BINARY_DIR}/flowdav-client-linux-amd64" "${BINARY_DIR}/flowdav-server-linux-amd64"

# Linux arm64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build "${BUILD_FLAGS[@]}" -o "${BINARY_DIR}/flowdav-client-linux-arm64" ./cmd/client
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build "${BUILD_FLAGS[@]}" -o "${BINARY_DIR}/flowdav-server-linux-arm64" ./cmd/server
zip -j "${RELEASE_DIR}/flowdav-linux-arm64.zip" "${BINARY_DIR}/flowdav-client-linux-arm64" "${BINARY_DIR}/flowdav-server-linux-arm64"

    echo "Done! Release archives in ${RELEASE_DIR}/"
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --to-bin)
            build_binaries
            exit 0
            ;;
        --image-to-bin)
            build_image
            echo "Extracting binaries from image..."
            CID=$(podman create flowdav:latest) || { echo "Failed to create container"; exit 1; }
            podman cp "$CID":/usr/local/bin/flowdav-client "./${BINARY_DIR}/flowdav-client" || { echo "Failed to copy flowdav-client"; podman rm "$CID" 2>/dev/null; exit 1; }
            podman cp "$CID":/usr/local/bin/flowdav-server "./${BINARY_DIR}/flowdav-server" || { echo "Failed to copy flowdav-server"; podman rm "$CID" 2>/dev/null; exit 1; }
            podman rm "$CID" || { echo "Warning: failed to remove container"; }
            chmod +x "${BINARY_DIR}/flowdav-client" "${BINARY_DIR}/flowdav-server"
            echo "Done! Binaries in ${BINARY_DIR}/"
            exit 0
            ;;
        --image)
            build_image
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            echo "Usage: $0 [--to-bin|--image-to-bin|--image]"
            exit 1
            ;;
    esac
    shift
done

# Default: build release archives (per AGENTS.md)
build_release
