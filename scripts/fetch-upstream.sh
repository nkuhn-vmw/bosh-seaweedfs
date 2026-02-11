#!/bin/bash
#
# Fetch and verify upstream SeaweedFS release assets.
# Usage: ./scripts/fetch-upstream.sh <version>
#
set -euo pipefail

if [ $# -lt 1 ]; then
  echo "Usage: $0 <version>"
  echo "Example: $0 3.82"
  exit 1
fi

VERSION=$1
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STAGING_DIR="${REPO_ROOT}/staged-artifacts"

echo "=== Fetching SeaweedFS ${VERSION} ==="

mkdir -p "${STAGING_DIR}/blobs"

# Download Linux amd64 binary
ASSET_NAME="linux_amd64_full.tar.gz"
DOWNLOAD_URL="https://github.com/seaweedfs/seaweedfs/releases/download/${VERSION}/${ASSET_NAME}"

echo "Downloading: ${DOWNLOAD_URL}"
curl -L -o "${STAGING_DIR}/blobs/seaweedfs-${VERSION}-linux-amd64.tar.gz" "${DOWNLOAD_URL}"

# Compute and record SHA-256
echo "Computing SHA-256 checksum..."
SHA256=$(sha256sum "${STAGING_DIR}/blobs/seaweedfs-${VERSION}-linux-amd64.tar.gz" | awk '{print $1}')
echo "${SHA256}  blobs/seaweedfs-${VERSION}-linux-amd64.tar.gz" > "${STAGING_DIR}/checksums.sha256"
echo "SHA-256: ${SHA256}"

# Verify the tarball contains the expected binary
echo "Verifying archive contents..."
tar tzf "${STAGING_DIR}/blobs/seaweedfs-${VERSION}-linux-amd64.tar.gz" | head -5

echo ""
echo "=== Fetch complete ==="
echo "Staged at: ${STAGING_DIR}/blobs/seaweedfs-${VERSION}-linux-amd64.tar.gz"
echo "Checksum:  ${STAGING_DIR}/checksums.sha256"
echo ""
echo "To add to BOSH release:"
echo "  bosh add-blob ${STAGING_DIR}/blobs/seaweedfs-${VERSION}-linux-amd64.tar.gz seaweedfs/seaweedfs-${VERSION}-linux-amd64.tar.gz"
