#!/bin/bash
# Get the next patch version from tile.yml

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE_DIR="$(dirname "$SCRIPT_DIR")"
TILE_YML="${RELEASE_DIR}/tile.yml"

# Extract current version
CURRENT_VERSION=$(grep "^version:" "$TILE_YML" | head -1 | sed 's/version:[[:space:]]*//' | tr -d '"' | tr -d "'")

if [ -z "$CURRENT_VERSION" ]; then
  echo "1.0.0"
  exit 0
fi

# Parse version components
IFS='.' read -ra VERSION_PARTS <<< "$CURRENT_VERSION"
MAJOR="${VERSION_PARTS[0]:-1}"
MINOR="${VERSION_PARTS[1]:-0}"
PATCH="${VERSION_PARTS[2]:-0}"

# Increment patch version
NEXT_PATCH=$((PATCH + 1))

echo "${MAJOR}.${MINOR}.${NEXT_PATCH}"
