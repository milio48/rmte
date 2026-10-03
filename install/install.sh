#!/usr/bin/env bash
set -euo pipefail

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${BLUE}==>${NC} Downloading RMTE portable binary..."

# Detect OS
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "${OS}" in
  linux*)  OS="linux" ;;
  darwin*) OS="darwin" ;;
  *)
    echo -e "${RED}Error: Unsupported operating system: ${OS}${NC}"
    exit 1
    ;;
esac

# Detect Architecture
ARCH="$(uname -m)"
case "${ARCH}" in
  x86_64|amd64)   ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *)
    echo -e "${RED}Error: Unsupported architecture: ${ARCH}${NC}"
    exit 1
    ;;
esac

BINARY_NAME="rmte-${OS}-${ARCH}"
DOWNLOAD_URL="https://github.com/milio48/rmte/releases/latest/download/${BINARY_NAME}"
TARGET_FILE="./rmte"
TEMP_FILE="./rmte.tmp.$$"

echo -e "${BLUE}==>${NC} Detected platform: ${GREEN}${OS}/${ARCH}${NC}"
echo -e "${BLUE}==>${NC} Downloading ${DOWNLOAD_URL}..."

trap 'rm -f "${TEMP_FILE}"' EXIT

if command -v curl >/dev/null 2>&1; then
  curl -fL --progress-bar "${DOWNLOAD_URL}" -o "${TEMP_FILE}"
elif command -v wget >/dev/null 2>&1; then
  wget -q --show-progress "${DOWNLOAD_URL}" -O "${TEMP_FILE}"
else
  echo -e "${RED}Error: curl or wget is required to download rmte.${NC}"
  exit 1
fi

chmod +x "${TEMP_FILE}"
mv "${TEMP_FILE}" "${TARGET_FILE}"

echo -e "${GREEN}==>${NC} Successfully downloaded portable binary to ${GREEN}${TARGET_FILE}${NC}"
echo -e "\nRun '${GREEN}./rmte help${NC}' or '${GREEN}./rmte serve${NC}' to get started!"
echo -e "\n${YELLOW}Tip:${NC} To make it available system-wide, optionally move it to /usr/local/bin/:"
echo "  sudo mv ./rmte /usr/local/bin/"
