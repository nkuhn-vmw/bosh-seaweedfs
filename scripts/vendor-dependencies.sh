#!/bin/bash
#
# Vendor all dependencies for air-gap BOSH release builds.
# This script must be run with internet access before building
# the release in an air-gapped environment.
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

echo "=== SeaweedFS BOSH Release: Vendoring Dependencies ==="
echo ""

# 1. Vendor Go modules for the broker
echo "[1/3] Vendoring Go modules for seaweedfs-broker..."
cd "${REPO_ROOT}/src/seaweedfs-broker"
go mod tidy
go mod vendor
echo "  Vendored $(find vendor -name '*.go' | wc -l | tr -d ' ') Go files"

# 2. Vendor Go modules for the smoke test app (if exists)
if [ -d "${REPO_ROOT}/src/smoke-test-app" ]; then
  echo ""
  echo "[2/3] Vendoring Go modules for smoke-test-app..."
  cd "${REPO_ROOT}/src/smoke-test-app"
  go mod tidy
  go mod vendor
  echo "  Vendored $(find vendor -name '*.go' | wc -l | tr -d ' ') Go files"
else
  echo ""
  echo "[2/3] Skipping smoke-test-app (not found)"
fi

# 3. Verify all blobs are present
echo ""
echo "[3/3] Checking BOSH blobs..."
cd "${REPO_ROOT}"

MISSING_BLOBS=0
check_blob() {
  local blob_path=$1
  local description=$2
  if [ -f "blobs/${blob_path}" ] || bosh blobs 2>/dev/null | grep -q "${blob_path}"; then
    echo "  OK: ${description} (${blob_path})"
  else
    echo "  MISSING: ${description} (${blob_path})"
    MISSING_BLOBS=$((MISSING_BLOBS + 1))
  fi
}

check_blob "seaweedfs/" "SeaweedFS binary"
check_blob "otel-collector/" "OTel Collector binary"
check_blob "cf-cli/" "CF CLI binary"

if [ ${MISSING_BLOBS} -gt 0 ]; then
  echo ""
  echo "WARNING: ${MISSING_BLOBS} blob(s) missing. Run 'bosh add-blob' for each before creating the release."
fi

echo ""
echo "=== Vendoring complete ==="
echo ""
echo "To build an air-gapped release:"
echo "  bosh create-release --force --tarball=seaweedfs-release.tgz"
