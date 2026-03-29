#!/usr/bin/env bash
set -euo pipefail

REPO="x7even/roctop"
BINARY="roctop"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# ── Platform check ────────────────────────────────────────────────────
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
if [ "$OS" != "linux" ]; then
    echo "error: roctop only supports Linux" >&2
    exit 1
fi

ARCH=$(uname -m)
case "$ARCH" in
    x86_64)          ARCH="amd64" ;;
    aarch64|arm64)   ARCH="arm64" ;;
    *)
        echo "error: unsupported architecture: $ARCH" >&2
        exit 1
        ;;
esac

# ── Fetch latest release version ─────────────────────────────────────
echo "Fetching latest roctop release..."
API_URL="https://api.github.com/repos/${REPO}/releases/latest"
VERSION=$(curl -fsSL "$API_URL" | grep '"tag_name"' | sed 's/.*"tag_name": *"\(.*\)".*/\1/')

if [ -z "$VERSION" ]; then
    echo "error: could not determine latest version (is GitHub reachable?)" >&2
    exit 1
fi

VERSION_NO_V="${VERSION#v}"
TARBALL="roctop_${VERSION_NO_V}_linux_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${TARBALL}"

echo "Installing roctop ${VERSION} (linux/${ARCH}) → ${INSTALL_DIR}/${BINARY}"

# ── Download and extract ──────────────────────────────────────────────
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

curl -fsSL "$URL" | tar xz -C "$TMP"

# ── Install ───────────────────────────────────────────────────────────
if [ -w "$INSTALL_DIR" ]; then
    mv "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
else
    echo "Root required to write to $INSTALL_DIR — running sudo mv"
    sudo mv "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
fi

chmod +x "$INSTALL_DIR/$BINARY"

echo ""
echo "roctop ${VERSION} installed successfully."
echo "Run: roctop"
