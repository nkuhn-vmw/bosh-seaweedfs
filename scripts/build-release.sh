#!/bin/bash
#
# Build SeaweedFS BOSH Release
#
# Usage:
#   ./scripts/build-release.sh [version]
#
# Examples:
#   ./scripts/build-release.sh           # Creates dev release with auto version
#   ./scripts/build-release.sh 1.0.64    # Creates release with specific version
#
# Output:
#   Prints the path to the created release tarball on the last line
#

set -eu

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE_DIR="$(dirname "$SCRIPT_DIR")"
OUTPUT_DIR="${RELEASE_DIR}/releases"

# Parse arguments
RELEASE_VERSION="${1:-}"

cd "$RELEASE_DIR"

echo "=== Building SeaweedFS BOSH Release ===" >&2

# Create output directory
mkdir -p "$OUTPUT_DIR"

# Step 1: Download and add required blobs
echo "" >&2
echo "=== Checking Blobs ===" >&2

# SeaweedFS binary
SEAWEEDFS_BLOB_VERSION="4.12"
if [[ ! -f "blobs/seaweedfs/linux_amd64.tar.gz" ]]; then
  mkdir -p blobs/seaweedfs
  echo "Downloading SeaweedFS ${SEAWEEDFS_BLOB_VERSION}..." >&2
  curl -sL "https://github.com/seaweedfs/seaweedfs/releases/download/${SEAWEEDFS_BLOB_VERSION}/linux_amd64.tar.gz" \
    -o "blobs/seaweedfs/linux_amd64.tar.gz"
  bosh add-blob "blobs/seaweedfs/linux_amd64.tar.gz" "seaweedfs/linux_amd64.tar.gz" >&2
else
  echo "SeaweedFS blob already present" >&2
fi

# Go toolchain for building the broker
GO_VERSION="1.21.6"
if [[ ! -f "blobs/golang/go${GO_VERSION}.linux-amd64.tar.gz" ]]; then
  mkdir -p blobs/golang
  echo "Downloading Go ${GO_VERSION}..." >&2
  curl -sL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
    -o "blobs/golang/go${GO_VERSION}.linux-amd64.tar.gz"
  bosh add-blob "blobs/golang/go${GO_VERSION}.linux-amd64.tar.gz" "golang/go${GO_VERSION}.linux-amd64.tar.gz" >&2
else
  echo "Go blob already present" >&2
fi

# OpenTelemetry Collector
OTEL_VERSION="0.96.0"
if [[ ! -f "blobs/otel-collector/otelcol-contrib.tar.gz" ]]; then
  mkdir -p blobs/otel-collector
  echo "Downloading OTel Collector ${OTEL_VERSION}..." >&2
  curl -sL "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v${OTEL_VERSION}/otelcol-contrib_${OTEL_VERSION}_linux_amd64.tar.gz" \
    -o "blobs/otel-collector/otelcol-contrib.tar.gz"
  bosh add-blob "blobs/otel-collector/otelcol-contrib.tar.gz" "otel-collector/otelcol-contrib.tar.gz" >&2
else
  echo "OTel Collector blob already present" >&2
fi

# AWS CLI v2 (for backup/restore S3 operations)
if [[ ! -f "blobs/backup-tools/awscli-exe-linux-x86_64.zip" ]]; then
  mkdir -p blobs/backup-tools
  echo "Downloading AWS CLI v2..." >&2
  curl -sL "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" \
    -o "blobs/backup-tools/awscli-exe-linux-x86_64.zip"
  bosh add-blob "blobs/backup-tools/awscli-exe-linux-x86_64.zip" "backup-tools/awscli-exe-linux-x86_64.zip" >&2
else
  echo "AWS CLI blob already present" >&2
fi

# CF CLI (for smoke tests)
CF_CLI_VERSION="8.8.2"
if [[ ! -f "blobs/cf-cli/cf8-cli-${CF_CLI_VERSION}-linux64.tgz" ]]; then
  mkdir -p blobs/cf-cli
  echo "Downloading CF CLI ${CF_CLI_VERSION}..." >&2
  curl -sL "https://packages.cloudfoundry.org/stable?release=linux64-binary&version=${CF_CLI_VERSION}&source=github-rel" \
    -o "blobs/cf-cli/cf8-cli-${CF_CLI_VERSION}-linux64.tgz"
  bosh add-blob "blobs/cf-cli/cf8-cli-${CF_CLI_VERSION}-linux64.tgz" "cf-cli/cf8-cli-${CF_CLI_VERSION}-linux64.tgz" >&2
else
  echo "CF CLI blob already present" >&2
fi

# PostgreSQL client (for filer postgres backend)
PG_VERSION="16.4"
if [[ ! -f "blobs/postgresql-client/postgresql-${PG_VERSION}.tar.bz2" ]]; then
  mkdir -p blobs/postgresql-client
  echo "Downloading PostgreSQL ${PG_VERSION} source..." >&2
  curl -sL "https://ftp.postgresql.org/pub/source/v${PG_VERSION}/postgresql-${PG_VERSION}.tar.bz2" \
    -o "blobs/postgresql-client/postgresql-${PG_VERSION}.tar.bz2"
  bosh add-blob "blobs/postgresql-client/postgresql-${PG_VERSION}.tar.bz2" "postgresql-client/postgresql-${PG_VERSION}.tar.bz2" >&2
else
  echo "PostgreSQL client blob already present" >&2
fi

# Step 2: Create broker source tarball
echo "" >&2
echo "=== Packaging Broker Source ===" >&2
mkdir -p blobs/seaweedfs-broker
tar -czf blobs/seaweedfs-broker/seaweedfs-broker.tar.gz -C src/seaweedfs-broker .
bosh add-blob "blobs/seaweedfs-broker/seaweedfs-broker.tar.gz" "seaweedfs-broker/seaweedfs-broker.tar.gz" >&2

# Step 3: Create the release
echo "" >&2
echo "=== Creating BOSH Release ===" >&2

if [[ -n "$RELEASE_VERSION" ]]; then
  TARBALL="${OUTPUT_DIR}/seaweedfs-${RELEASE_VERSION}.tgz"
  bosh create-release --force --version="$RELEASE_VERSION" --tarball="$TARBALL" >&2
else
  # Dev release with timestamp
  TIMESTAMP=$(date +%Y%m%d%H%M%S)
  TARBALL="${OUTPUT_DIR}/seaweedfs-dev-${TIMESTAMP}.tgz"
  bosh create-release --force --tarball="$TARBALL" >&2
fi

# Step 4: Report results
echo "" >&2
echo "=== Build Complete ===" >&2
echo "Release created: $TARBALL" >&2
echo "File size: $(du -h "$TARBALL" | cut -f1)" >&2

# List jobs in release
echo "" >&2
echo "Jobs included:" >&2
tar -tzf "$TARBALL" | grep -E "^jobs/.*\.tgz$" | sed 's|jobs/||' | sed 's|\.tgz||' | while read job; do
  echo "  - $job" >&2
done

# Output the tarball path (for use by other scripts)
echo "$TARBALL"
