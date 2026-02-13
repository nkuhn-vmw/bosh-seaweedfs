#!/bin/bash
#
# Build SeaweedFS Tanzu Tile
#
# Usage:
#   ./scripts/build-tile.sh [version] [--release path/to/seaweedfs-release.tgz]
#
# Examples:
#   ./scripts/build-tile.sh                    # Auto-increment version, use default release path
#   ./scripts/build-tile.sh 1.0.64             # Specific version
#   ./scripts/build-tile.sh --release /path/to/release.tgz
#

set -eu

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"
TILE_CMD="${TILE_CMD:-$(command -v tile 2>/dev/null || echo "/Users/nkuhn/Library/Python/3.9/bin/tile")}"

# Parse arguments
TILE_VERSION=""
RELEASE_PATH=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --release)
      RELEASE_PATH="$2"
      shift 2
      ;;
    --help|-h)
      echo "Usage: $0 [version] [--release path/to/seaweedfs-release.tgz]"
      echo ""
      echo "Arguments:"
      echo "  version              Tile version (optional, auto-increments if not specified)"
      echo "  --release PATH       Path to seaweedfs BOSH release tarball"
      echo ""
      echo "Environment variables:"
      echo "  TILE_CMD             Path to tile-generator command"
      exit 0
      ;;
    *)
      if [[ -z "$TILE_VERSION" && "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        TILE_VERSION="$1"
      else
        echo "Unknown argument: $1"
        exit 1
      fi
      shift
      ;;
  esac
done

cd "$REPO_DIR"

# Determine version
if [[ -z "$TILE_VERSION" ]]; then
  TILE_VERSION=$("${SCRIPT_DIR}/get-next-version.sh")
  echo "Auto-incrementing to version: $TILE_VERSION"
fi

echo "=== Building SeaweedFS Tile ==="
echo "Tile Version: $TILE_VERSION"
echo "Repo Directory: $REPO_DIR"

# Step 1: Get the BOSH release
echo ""
echo "=== Obtaining BOSH Release ==="

if [[ -n "$RELEASE_PATH" ]]; then
  # Use provided release path
  if [[ ! -f "$RELEASE_PATH" ]]; then
    echo "Error: Release file not found: $RELEASE_PATH"
    exit 1
  fi
  echo "Using provided release: $RELEASE_PATH"
  cp "$RELEASE_PATH" resources/seaweedfs-release.tgz
else
  # Build from this repo
  echo "Building BOSH release..."
  RELEASE_FILE=$("${SCRIPT_DIR}/build-release.sh" "$TILE_VERSION" 2>&1 | tail -1)

  if [[ ! -f "$RELEASE_FILE" ]]; then
    echo "Error: Failed to build BOSH release"
    exit 1
  fi

  echo "Copying release to resources/"
  cp "$RELEASE_FILE" resources/seaweedfs-release.tgz
fi

# Step 2: Download dependency releases if needed
echo ""
echo "=== Checking Dependency Releases ==="

BPM_VERSION="1.1.21"
if [[ ! -f "resources/bpm-release.tgz" ]]; then
  echo "Downloading BPM release ${BPM_VERSION}..."
  curl -sL "https://bosh.io/d/github.com/cloudfoundry/bpm-release?v=${BPM_VERSION}" \
    -o "resources/bpm-release.tgz"
else
  echo "BPM release already present"
fi

ROUTING_VERSION="0.283.0"
if [[ ! -f "resources/routing-release.tgz" ]]; then
  echo "Downloading Routing release ${ROUTING_VERSION}..."
  curl -sL "https://bosh.io/d/github.com/cloudfoundry/routing-release?v=${ROUTING_VERSION}" \
    -o "resources/routing-release.tgz"
else
  echo "Routing release already present"
fi

# Step 3: Update tile.yml version
echo ""
echo "=== Updating tile.yml ==="
if [[ "$OSTYPE" == "darwin"* ]]; then
  sed -i '' "s/^version: .*/version: \"$TILE_VERSION\"/" tile.yml
else
  sed -i "s/^version: .*/version: \"$TILE_VERSION\"/" tile.yml
fi
echo "Updated tile.yml to version $TILE_VERSION"

# Step 4: Build tile
echo ""
echo "=== Building Tile with tile-generator ==="
"$TILE_CMD" build "$TILE_VERSION"

# Step 5: Post-process tile metadata
# tile-generator cannot output boolean false for run_post_deploy_errand_default
# because its Python code uses `if job.get(...)` which filters out falsy values.
# We pass "false" as a quoted string, then fix the metadata here.
echo ""
echo "=== Post-processing tile metadata ==="
TILE_FILE="product/seaweedfs-${TILE_VERSION}.pivotal"
if [[ -f "$TILE_FILE" ]]; then
  PATCH_DIR=$(mktemp -d)
  unzip -q -o "$TILE_FILE" metadata/seaweedfs.yml -d "$PATCH_DIR"
  if grep -q "run_post_deploy_errand_default: 'false'" "$PATCH_DIR/metadata/seaweedfs.yml"; then
    if [[ "$OSTYPE" == "darwin"* ]]; then
      sed -i '' "s/run_post_deploy_errand_default: 'false'/run_post_deploy_errand_default: false/" "$PATCH_DIR/metadata/seaweedfs.yml"
    else
      sed -i "s/run_post_deploy_errand_default: 'false'/run_post_deploy_errand_default: false/" "$PATCH_DIR/metadata/seaweedfs.yml"
    fi
    # Update the .pivotal archive with patched metadata
    (cd "$PATCH_DIR" && zip -q "$REPO_DIR/$TILE_FILE" metadata/seaweedfs.yml)
    echo "Patched errand default: 'false' -> false (boolean)"
  else
    echo "No errand default patching needed"
  fi
  rm -rf "$PATCH_DIR"
fi

# Step 6: Report results
echo ""
echo "=== Build Complete ==="

if [[ -f "$TILE_FILE" ]]; then
  echo "Tile created: $TILE_FILE"
  echo "File size: $(du -h "$TILE_FILE" | cut -f1)"
  echo ""
  echo "Tile contents:"
  unzip -l "$TILE_FILE" | head -12
  echo ""
  echo "To install:"
  echo "  1. Upload $TILE_FILE to Tanzu Operations Manager"
  echo "  2. Configure tile settings"
  echo "  3. Apply changes"
else
  echo "Error: Tile file not created"
  exit 1
fi
