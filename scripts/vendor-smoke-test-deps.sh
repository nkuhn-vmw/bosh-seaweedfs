#!/bin/bash
#
# Download Python dependencies for smoke test app
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE_DIR="${SCRIPT_DIR}/.."
VENDOR_DIR="${RELEASE_DIR}/src/smoke-tests-vendor"

echo "Downloading Python dependencies for smoke test app..."

# Clean and create vendor directory
rm -rf "${VENDOR_DIR}"
mkdir -p "${VENDOR_DIR}"

# Create a temporary requirements file
cat > "${VENDOR_DIR}/requirements.txt" << 'EOF'
flask>=2.0.0
boto3>=1.26.0
botocore>=1.29.0
urllib3<2.0
EOF

# Download wheels for linux (cflinuxfs3/cflinuxfs4 are linux-based)
echo "Downloading wheels..."
pip3 download \
    --dest "${VENDOR_DIR}" \
    --platform manylinux2014_x86_64 \
    --platform manylinux_2_17_x86_64 \
    --platform linux_x86_64 \
    --platform any \
    --python-version 3.10 \
    --only-binary=:all: \
    -r "${VENDOR_DIR}/requirements.txt" || {
    echo "Falling back to source distributions..."
    pip3 download \
        --dest "${VENDOR_DIR}" \
        --no-binary=:all: \
        -r "${VENDOR_DIR}/requirements.txt"
}

# Remove the requirements.txt from vendor dir (it's in the templates)
rm -f "${VENDOR_DIR}/requirements.txt"

echo ""
echo "Vendored dependencies:"
ls -la "${VENDOR_DIR}"

echo ""
echo "Dependencies downloaded to: ${VENDOR_DIR}"
echo "These will be included in the BOSH release."
