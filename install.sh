#!/bin/sh
# Install ytd from the latest GitHub release.
#   curl -fsSL https://raw.githubusercontent.com/Sn0wye/ytd/main/install.sh | sh
# Env: YTD_VERSION (e.g. v1.0.0, default latest), YTD_INSTALL_DIR (default /usr/local/bin)
set -eu

repo="Sn0wye/ytd"
install_dir="${YTD_INSTALL_DIR:-/usr/local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  darwin|linux) ;;
  *) echo "ytd: unsupported OS: $os (on Windows, download the zip from https://github.com/$repo/releases)" >&2; exit 1 ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "ytd: unsupported architecture: $arch" >&2; exit 1 ;;
esac

if [ -n "${YTD_VERSION:-}" ]; then
  base="https://github.com/$repo/releases/download/$YTD_VERSION"
else
  base="https://github.com/$repo/releases/latest/download"
fi
archive="ytd_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $archive..."
curl -fsSL "$base/$archive" -o "$tmp/$archive"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"

expected=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
else
  actual=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)
fi
if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
  echo "ytd: checksum mismatch for $archive" >&2
  exit 1
fi

tar -xzf "$tmp/$archive" -C "$tmp" ytd

if [ -w "$install_dir" ]; then
  install -m 755 "$tmp/ytd" "$install_dir/ytd"
else
  echo "Installing to $install_dir (needs sudo)..."
  sudo install -m 755 "$tmp/ytd" "$install_dir/ytd"
fi

echo "Installed: $("$install_dir/ytd" version | head -1)"
