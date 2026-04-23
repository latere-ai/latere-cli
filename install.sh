#!/usr/bin/env sh
# Install the latere CLI. Downloads the latest GitHub release binary for the
# current platform, verifies the checksum, and places it on PATH.
#
# Usage:
#   curl -fsSL https://latere.ai/install.sh | sh
#   curl -fsSL https://latere.ai/install.sh | sh -s -- v0.1.0      # pinned
#   curl -fsSL https://latere.ai/install.sh | PREFIX=$HOME/.local sh
set -eu

REPO="latere-ai/latere-cli"
BIN="latere"
VERSION="${1:-latest}"
PREFIX="${PREFIX:-/usr/local}"
BIN_DIR="${PREFIX}/bin"

err() { printf 'latere-install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || err "required command not found: $1"; }

need uname
need tar
need install
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1"; }
  fetch_to() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO- "$1"; }
  fetch_to() { wget -qO "$2" "$1"; }
else
  err "need curl or wget"
fi

os="$(uname -s)"
case "$os" in
  Linux)  os_tag=linux ;;
  Darwin) os_tag=darwin ;;
  *)      err "unsupported OS: $os (use release archive on github.com/${REPO}/releases)" ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch_tag=amd64 ;;
  arm64|aarch64) arch_tag=arm64 ;;
  *) err "unsupported arch: $arch" ;;
esac

if [ "$VERSION" = "latest" ]; then
  VERSION="$(fetch "https://api.github.com/repos/${REPO}/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
  [ -n "$VERSION" ] || err "could not resolve latest release (maybe none published yet)"
fi
VERSION_STRIPPED="${VERSION#v}"

asset="${BIN}_${VERSION_STRIPPED}_${os_tag}_${arch_tag}.tar.gz"
url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
sum_url="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

printf 'latere-install: downloading %s\n' "$asset" >&2
fetch_to "$url" "${tmp}/${asset}"

if fetch "$sum_url" > "${tmp}/checksums.txt" 2>/dev/null; then
  expected="$(grep " ${asset}$" "${tmp}/checksums.txt" | awk '{print $1}')"
  if [ -n "$expected" ] && command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "${tmp}/${asset}" | awk '{print $1}')"
    [ "$expected" = "$actual" ] || err "checksum mismatch for ${asset}"
  fi
fi

tar -xzf "${tmp}/${asset}" -C "$tmp"

if [ ! -w "$BIN_DIR" ] && [ "$(id -u)" -ne 0 ]; then
  sudo install -m 0755 "${tmp}/${BIN}" "${BIN_DIR}/${BIN}"
else
  install -m 0755 "${tmp}/${BIN}" "${BIN_DIR}/${BIN}"
fi

printf 'latere-install: installed %s %s to %s/%s\n' "$BIN" "$VERSION" "$BIN_DIR" "$BIN" >&2
"${BIN_DIR}/${BIN}" --version
