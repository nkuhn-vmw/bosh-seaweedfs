#!/bin/bash
set -e

SEAWEEDFS_VERSION="${1:-4.07}"
DOWNLOAD_DIR="$(mktemp -d)"
BLOB_FILE="${DOWNLOAD_DIR}/linux_amd64.tar.gz"

echo "Downloading SeaweedFS ${SEAWEEDFS_VERSION}..."
curl -L -o "${BLOB_FILE}" \
  "https://github.com/seaweedfs/seaweedfs/releases/download/${SEAWEEDFS_VERSION}/linux_amd64.tar.gz"

echo "Verifying download..."
if [ -f "${BLOB_FILE}" ]; then
  echo "Download successful: $(ls -lh ${BLOB_FILE})"
else
  echo "Download failed!"
  exit 1
fi

echo "Adding blob to BOSH release..."
cd "$(dirname "$0")/.."
bosh add-blob "${BLOB_FILE}" "seaweedfs/linux_amd64.tar.gz"

echo "Cleaning up..."
rm -rf "${DOWNLOAD_DIR}"

echo "Done! Blob added. Run 'bosh create-release' to create the release."
