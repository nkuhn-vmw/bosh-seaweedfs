#!/bin/bash

set -eu

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE_DIR="$(dirname "$SCRIPT_DIR")"
TILE_DIR="${RELEASE_DIR}/tile"
BUILD_DIR="${RELEASE_DIR}/build"
OUTPUT_DIR="${RELEASE_DIR}/product"

# Configuration
TILE_NAME="seaweedfs"
TILE_VERSION="${TILE_VERSION:-1.0.0}"
SEAWEEDFS_RELEASE_VERSION="${SEAWEEDFS_RELEASE_VERSION:-1.0.0}"

# Dependency releases (these should be downloaded separately)
BPM_VERSION="${BPM_VERSION:-1.1.21}"
ROUTING_VERSION="${ROUTING_VERSION:-0.283.0}"

echo "=== Building SeaweedFS Tile ==="
echo "Tile Version: ${TILE_VERSION}"
echo "SeaweedFS Release Version: ${SEAWEEDFS_RELEASE_VERSION}"

# Clean previous builds
rm -rf "${BUILD_DIR}" "${OUTPUT_DIR}"
mkdir -p "${BUILD_DIR}" "${OUTPUT_DIR}"

# Step 1: Create the BOSH release
echo ""
echo "=== Creating BOSH Release ==="
cd "${RELEASE_DIR}"

# Download and add required blobs
echo "Checking and downloading required blobs..."

# SeaweedFS binary
SEAWEEDFS_BLOB_VERSION="4.07"
if [ ! -f "blobs/seaweedfs/linux_amd64.tar.gz" ]; then
  mkdir -p blobs/seaweedfs
  echo "Downloading SeaweedFS ${SEAWEEDFS_BLOB_VERSION}..."
  curl -sL "https://github.com/seaweedfs/seaweedfs/releases/download/${SEAWEEDFS_BLOB_VERSION}/linux_amd64.tar.gz" \
    -o "blobs/seaweedfs/linux_amd64.tar.gz"
  bosh add-blob "blobs/seaweedfs/linux_amd64.tar.gz" "seaweedfs/linux_amd64.tar.gz"
else
  echo "SeaweedFS blob already present"
fi

# Go toolchain for building the broker
GO_VERSION="1.21.6"
if [ ! -f "blobs/golang/go${GO_VERSION}.linux-amd64.tar.gz" ]; then
  mkdir -p blobs/golang
  echo "Downloading Go ${GO_VERSION}..."
  curl -sL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
    -o "blobs/golang/go${GO_VERSION}.linux-amd64.tar.gz"
  bosh add-blob "blobs/golang/go${GO_VERSION}.linux-amd64.tar.gz" "golang/go${GO_VERSION}.linux-amd64.tar.gz"
else
  echo "Go blob already present"
fi

# Create broker source tarball
echo "Creating broker source tarball..."
mkdir -p blobs/seaweedfs-broker
tar -czf blobs/seaweedfs-broker/seaweedfs-broker.tar.gz -C src/seaweedfs-broker .
bosh add-blob "blobs/seaweedfs-broker/seaweedfs-broker.tar.gz" "seaweedfs-broker/seaweedfs-broker.tar.gz"

# Create the release
echo "Creating BOSH release..."
bosh create-release --force --version="${SEAWEEDFS_RELEASE_VERSION}" \
  --tarball="${BUILD_DIR}/seaweedfs-${SEAWEEDFS_RELEASE_VERSION}.tgz"

# Step 2: Download dependency releases
echo ""
echo "=== Downloading Dependency Releases ==="

# BPM Release
if [ ! -f "${BUILD_DIR}/bpm-${BPM_VERSION}.tgz" ]; then
  echo "Downloading BPM release ${BPM_VERSION}..."
  curl -sL "https://bosh.io/d/github.com/cloudfoundry/bpm-release?v=${BPM_VERSION}" \
    -o "${BUILD_DIR}/bpm-${BPM_VERSION}.tgz" || {
    echo "Warning: Could not download BPM release. Please download manually."
  }
fi

# Routing Release
if [ ! -f "${BUILD_DIR}/routing-${ROUTING_VERSION}.tgz" ]; then
  echo "Downloading Routing release ${ROUTING_VERSION}..."
  curl -sL "https://bosh.io/d/github.com/cloudfoundry/routing-release?v=${ROUTING_VERSION}" \
    -o "${BUILD_DIR}/routing-${ROUTING_VERSION}.tgz" || {
    echo "Warning: Could not download Routing release. Please download manually."
  }
fi

# Step 3: Prepare tile structure
echo ""
echo "=== Preparing Tile Structure ==="
TILE_BUILD="${BUILD_DIR}/tile"
mkdir -p "${TILE_BUILD}/metadata"
mkdir -p "${TILE_BUILD}/releases"
mkdir -p "${TILE_BUILD}/migrations"

# Copy metadata
cp "${TILE_DIR}/metadata/tile.yml" "${TILE_BUILD}/metadata/"

# Update version in metadata (cross-platform sed)
if [[ "$OSTYPE" == "darwin"* ]]; then
  sed -i '' "s/product_version:.*/product_version: \"${TILE_VERSION}\"/" "${TILE_BUILD}/metadata/tile.yml"
  sed -i '' "s/version: \"1.0.0\"/version: \"${SEAWEEDFS_RELEASE_VERSION}\"/" "${TILE_BUILD}/metadata/tile.yml"
else
  sed -i "s/product_version:.*/product_version: \"${TILE_VERSION}\"/" "${TILE_BUILD}/metadata/tile.yml"
  sed -i "s/version: \"1.0.0\"/version: \"${SEAWEEDFS_RELEASE_VERSION}\"/" "${TILE_BUILD}/metadata/tile.yml"
fi

# Copy releases
cp "${BUILD_DIR}/seaweedfs-${SEAWEEDFS_RELEASE_VERSION}.tgz" \
   "${TILE_BUILD}/releases/seaweedfs-${SEAWEEDFS_RELEASE_VERSION}.tgz"

if [ -f "${BUILD_DIR}/bpm-${BPM_VERSION}.tgz" ]; then
  cp "${BUILD_DIR}/bpm-${BPM_VERSION}.tgz" "${TILE_BUILD}/releases/"
fi

if [ -f "${BUILD_DIR}/routing-${ROUTING_VERSION}.tgz" ]; then
  cp "${BUILD_DIR}/routing-${ROUTING_VERSION}.tgz" "${TILE_BUILD}/releases/"
fi

# Create empty migrations directory with placeholder
touch "${TILE_BUILD}/migrations/.keep"

# Step 4: Create the tile (.pivotal file)
echo ""
echo "=== Creating Tile Package ==="
TILE_FILE="${OUTPUT_DIR}/${TILE_NAME}-${TILE_VERSION}.pivotal"

cd "${TILE_BUILD}"
zip -r "${TILE_FILE}" . -x "*.DS_Store" -x "*/.keep"

echo ""
echo "=== Build Complete ==="
echo "Tile created: ${TILE_FILE}"
echo ""
echo "File size: $(du -h "${TILE_FILE}" | cut -f1)"
echo ""
echo "To install:"
echo "  1. Upload to Tanzu Operations Manager"
echo "  2. Configure the tile settings"
echo "  3. Apply changes"
echo ""

# Step 5: Verify the tile
echo "=== Tile Contents ==="
unzip -l "${TILE_FILE}" | head -20
echo "..."
