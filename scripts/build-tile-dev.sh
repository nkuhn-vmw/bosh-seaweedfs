#!/bin/bash

set -eu

# Development tile builder - creates a tile structure without downloading external releases
# For production, use build-tile.sh which downloads all dependencies

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE_DIR="$(dirname "$SCRIPT_DIR")"
TILE_DIR="${RELEASE_DIR}/tile"
BUILD_DIR="${RELEASE_DIR}/build"
OUTPUT_DIR="${RELEASE_DIR}/product"

TILE_NAME="seaweedfs"
TILE_VERSION="${TILE_VERSION:-1.0.0-dev}"

echo "=== Building SeaweedFS Development Tile ==="
echo "This creates a tile structure for development/testing."
echo "For production use, run build-tile.sh instead."
echo ""

# Clean previous builds
rm -rf "${BUILD_DIR}" "${OUTPUT_DIR}"
mkdir -p "${BUILD_DIR}/tile/metadata"
mkdir -p "${BUILD_DIR}/tile/releases"
mkdir -p "${BUILD_DIR}/tile/migrations"
mkdir -p "${OUTPUT_DIR}"

# Create BOSH release (dev version)
echo "Creating BOSH development release..."
cd "${RELEASE_DIR}"
bosh create-release --force --tarball="${BUILD_DIR}/tile/releases/seaweedfs-${TILE_VERSION}.tgz" 2>/dev/null || {
  echo "Warning: Could not create BOSH release. Creating placeholder..."
  touch "${BUILD_DIR}/tile/releases/seaweedfs-${TILE_VERSION}.tgz"
}

# Copy and update metadata
cp "${TILE_DIR}/metadata/tile.yml" "${BUILD_DIR}/tile/metadata/"
# Cross-platform sed
if [[ "$OSTYPE" == "darwin"* ]]; then
  sed -i '' "s/product_version:.*/product_version: \"${TILE_VERSION}\"/" \
    "${BUILD_DIR}/tile/metadata/tile.yml"
else
  sed -i "s/product_version:.*/product_version: \"${TILE_VERSION}\"/" \
    "${BUILD_DIR}/tile/metadata/tile.yml"
fi

# Create placeholder releases for dependencies
echo "Creating placeholder release files..."
touch "${BUILD_DIR}/tile/releases/bpm-1.1.21.tgz"
touch "${BUILD_DIR}/tile/releases/routing-0.283.0.tgz"
touch "${BUILD_DIR}/tile/migrations/.keep"

# Create the tile
TILE_FILE="${OUTPUT_DIR}/${TILE_NAME}-${TILE_VERSION}.pivotal"
cd "${BUILD_DIR}/tile"
zip -r "${TILE_FILE}" . -x "*.DS_Store" -x "*/.keep"

echo ""
echo "=== Development Tile Created ==="
echo "Output: ${TILE_FILE}"
echo ""
echo "Note: This tile contains placeholder dependency releases."
echo "For a production tile, use build-tile.sh which downloads"
echo "the actual BPM and Routing releases from bosh.io."
