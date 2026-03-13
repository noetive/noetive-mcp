#!/bin/sh
set -e

REPO="noetive/noetive-mcp"
BINARY="noetive-mcp"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

case "$OS" in
  linux|darwin) ;;
  mingw*|msys*|cygwin*)
    OS="windows"
    BINARY="${BINARY}.exe"
    ;;
  *)
    echo "Unsupported OS: $OS" >&2
    exit 1
    ;;
esac

ASSET="${BINARY}-${OS}-${ARCH}"
if [ "$OS" = "windows" ]; then
  ASSET="${ASSET}.exe"
fi

URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"

mkdir -p bin
echo "Downloading ${ASSET}..."
if command -v curl >/dev/null 2>&1; then
  curl -fsSL -o "bin/${BINARY}" "$URL"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "bin/${BINARY}" "$URL"
else
  echo "Neither curl nor wget found" >&2
  exit 1
fi

chmod +x "bin/${BINARY}"
echo "Installed ${BINARY} to bin/"
