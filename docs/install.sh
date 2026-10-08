#!/bin/sh
# RMTE Installer & Launcher
# Usage:
#   curl -sSf https://rmte.biz.id/install.sh | sh                   # Default: Download to current directory
#   curl -sSf https://rmte.biz.id/install.sh | sh -s run            # Download to /tmp and run immediately
#   curl -sSf https://rmte.biz.id/install.sh | sh -s run -q         # Download to /tmp and run in background daemon (quiet)
#   curl -sSf https://rmte.biz.id/install.sh | sh -s download       # Download to current directory
#   curl -sSf https://rmte.biz.id/install.sh | sh -s install        # Install to ~/.local/bin or /usr/local/bin
#   curl -sSf https://rmte.biz.id/install.sh | sh -s download run   # Download to current directory and run
#   curl -sSf https://rmte.biz.id/install.sh | sh -s install run    # Install to bin and run

set -eu

# Color codes (POSIX printf safe)
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

DO_DOWNLOAD=0
DO_INSTALL=0
DO_RUN=0
QUIET_MODE=0
CUSTOM_DIR=""
EXTRA_ARGS=""

while [ $# -gt 0 ]; do
  case "$1" in
    run)
      DO_RUN=1
      shift
      ;;
    download)
      DO_DOWNLOAD=1
      shift
      ;;
    install)
      DO_INSTALL=1
      shift
      ;;
    -q|--quiet)
      QUIET_MODE=1
      EXTRA_ARGS="${EXTRA_ARGS} -q"
      shift
      ;;
    --dir=*)
      CUSTOM_DIR="${1#*=}"
      shift
      ;;
    --dir)
      shift
      if [ $# -gt 0 ]; then
        CUSTOM_DIR="$1"
        shift
      fi
      ;;
    *)
      EXTRA_ARGS="${EXTRA_ARGS} $1"
      shift
      ;;
  esac
done

# Default behaviors:
# If no action specified, default to download.
# If only -q specified without run/download/install, default to ephemeral run in quiet mode.
if [ "$DO_DOWNLOAD" -eq 0 ] && [ "$DO_INSTALL" -eq 0 ] && [ "$DO_RUN" -eq 0 ]; then
  if [ "$QUIET_MODE" -eq 1 ]; then
    DO_RUN=1
  else
    DO_DOWNLOAD=1
  fi
fi

# If only 'run' was specified without download/install, target temporary folder
if [ "$DO_DOWNLOAD" -eq 0 ] && [ "$DO_INSTALL" -eq 0 ] && [ "$DO_RUN" -eq 1 ]; then
  TARGET_DIR="${TMPDIR:-/tmp}/rmte-bin"
elif [ "$DO_INSTALL" -eq 1 ]; then
  if [ "$(id -u)" -eq 0 ]; then
    TARGET_DIR="/usr/local/bin"
  else
    TARGET_DIR="${HOME}/.local/bin"
  fi
else
  # Default: download to current directory
  TARGET_DIR="${CUSTOM_DIR:-.}"
fi

# Detect OS
EXE_EXT=""
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "${OS}" in
  linux*)  OS="linux" ;;
  darwin*) OS="darwin" ;;
  mingw*|msys*|cygwin*)
    OS="windows"
    EXE_EXT=".exe"
    ;;
  *)
    printf "${RED}Error: Unsupported operating system: %s${NC}\n" "${OS}" >&2
    exit 1
    ;;
esac

# Detect Architecture
ARCH="$(uname -m)"
case "${ARCH}" in
  x86_64|amd64)   ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *)
    printf "${RED}Error: Unsupported architecture: %s${NC}\n" "${ARCH}" >&2
    exit 1
    ;;
esac

BINARY_NAME="rmte-${OS}-${ARCH}${EXE_EXT}"
DOWNLOAD_URL="https://github.com/milio48/rmte/releases/latest/download/${BINARY_NAME}"
TARGET_FILE="${TARGET_DIR}/rmte${EXE_EXT}"

mkdir -p "${TARGET_DIR}"

if [ "$QUIET_MODE" -eq 0 ]; then
  printf "${BLUE}==>${NC} Detected platform: ${GREEN}%s/%s${NC}\n" "${OS}" "${ARCH}"
  printf "${BLUE}==>${NC} Downloading ${CYAN}%s${NC}...\n" "${DOWNLOAD_URL}"
fi

TEMP_FILE="${TARGET_DIR}/rmte.tmp.$$"
trap 'rm -f "${TEMP_FILE}"' EXIT INT TERM

if command -v curl >/dev/null 2>&1; then
  if [ "$QUIET_MODE" -eq 1 ]; then
    curl -sSfL "${DOWNLOAD_URL}" -o "${TEMP_FILE}"
  else
    curl -fL --progress-bar "${DOWNLOAD_URL}" -o "${TEMP_FILE}"
  fi
elif command -v wget >/dev/null 2>&1; then
  if [ "$QUIET_MODE" -eq 1 ]; then
    wget -q "${DOWNLOAD_URL}" -O "${TEMP_FILE}"
  else
    wget -q --show-progress "${DOWNLOAD_URL}" -O "${TEMP_FILE}"
  fi
else
  printf "${RED}Error: curl or wget is required to download rmte.${NC}\n" >&2
  exit 1
fi

chmod +x "${TEMP_FILE}"
mv -f "${TEMP_FILE}" "${TARGET_FILE}"
trap - EXIT INT TERM

if [ "$QUIET_MODE" -eq 0 ]; then
  printf "${GREEN}==>${NC} Ready: ${GREEN}%s${NC}\n" "${TARGET_FILE}"
fi

# If installed to ~/.local/bin, verify it is in PATH
if [ "$DO_INSTALL" -eq 1 ]; then
  case ":${PATH}:" in
    *":${TARGET_DIR}:"*) ;;
    *)
      printf "\n${YELLOW}Tip:${NC} %s is not currently in your \$PATH.\n" "${TARGET_DIR}"
      printf "Add it to your shell config (~/.bashrc or ~/.zshrc):\n"
      printf "  ${CYAN}export PATH=\"%s:\$PATH\"${NC}\n\n" "${TARGET_DIR}"
      ;;
  esac
fi

# Run binary if requested
if [ "$DO_RUN" -eq 1 ]; then
  if [ "$QUIET_MODE" -eq 0 ]; then
    printf "${BLUE}==>${NC} Launching RMTE...\n\n"
  fi
  if [ -n "${EXTRA_ARGS}" ]; then
    # shellcheck disable=SC2086
    exec "${TARGET_FILE}" ${EXTRA_ARGS}
  else
    exec "${TARGET_FILE}"
  fi
else
  printf "\nRun '${GREEN}%s help${NC}' or '${GREEN}%s${NC}' to get started!\n" "${TARGET_FILE}" "${TARGET_FILE}"
fi
