#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

# NOTE: This script is kept for standalone release builds outside CI.
# For normal development, use: ./scripts/build.sh [--to-bin|--image|--image-to-bin]
# This script requires Go to be installed locally (no Podman needed).
VERSION="${VERSION:-v1.0.0}"
RELEASE_DIR="${PROJECT_DIR}/release"
BUILD_FLAGS=(-trimpath "-ldflags=-s -w")

# Clean previous releases
rm -rf "$RELEASE_DIR"
mkdir -p "$RELEASE_DIR"

platforms=(
    "linux/amd64"
    "linux/arm64"
    "windows/amd64"
    "darwin/amd64"
    "darwin/arm64"
)

echo "Building binaries for version $VERSION..."

for platform in "${platforms[@]}"; do
    OS=${platform%/*}
    ARCH=${platform#*/}

    SUFFIX=""
    if [ "$OS" == "windows" ]; then
        SUFFIX=".exe"
    fi

    FOLDER_NAME="flowdav-${VERSION}-${OS}-${ARCH}"
    OUTPUT_PATH="${RELEASE_DIR}/${FOLDER_NAME}"
    mkdir -p "$OUTPUT_PATH"

    echo "Building $OS/$ARCH..."

    # Build Client
    CGO_ENABLED=0 GOOS=$OS GOARCH=$ARCH go build "${BUILD_FLAGS[@]}" -o "$OUTPUT_PATH/flowdav-client$SUFFIX" ./cmd/client || {
        echo "ERROR: Failed to build flowdav-client for $OS/$ARCH"
        exit 1
    }
    CGO_ENABLED=0 GOOS=$OS GOARCH=$ARCH go build "${BUILD_FLAGS[@]}" -o "$OUTPUT_PATH/flowdav-server$SUFFIX" ./cmd/server || {
        echo "ERROR: Failed to build flowdav-server for $OS/$ARCH"
        exit 1
    }

    # Copy Example Configs and README
    if ! cp "${SCRIPT_DIR}/../configs/"*.json.example "$OUTPUT_PATH/" 2>/dev/null; then
        echo "WARNING: No example configs found to copy"
    fi
    if ! cp "${SCRIPT_DIR}/../README.md" "$OUTPUT_PATH/" 2>/dev/null; then
        echo "WARNING: README.md not found"
    fi

    # Package it up
    (cd "$RELEASE_DIR" && tar -czf "${FOLDER_NAME}.tar.gz" "$FOLDER_NAME") || {
        echo "ERROR: Failed to create archive for $FOLDER_NAME"
        exit 1
    }

    # Cleanup folder (keep only archive)
    rm -rf "$OUTPUT_PATH"
done

echo "Done! Release packages created in $RELEASE_DIR/:"
ls -lh "$RELEASE_DIR"
