#!/bin/sh
set -eu
version=${RADCHAT_VERSION:-v0.4.0}
case "$version" in v[0-9]*.[0-9]*.[0-9]*) ;; *) printf 'Invalid release version\n' >&2; exit 1;; esac
case "$(uname -s)" in Darwin) platform=darwin;; Linux) platform=linux;; *) printf 'Use the Windows release zip.\n' >&2; exit 1;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64;; x86_64|amd64) arch=amd64;; *) printf 'Unsupported architecture\n' >&2; exit 1;; esac
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
asset="radchat-${version}-${platform}-${arch}.tar.gz"
base="https://github.com/dmikey/p2p-chat/releases/download/${version}"
curl -fL --proto '=https' --tlsv1.2 "$base/$asset" -o "$tmp/$asset"
curl -fL --proto '=https' --tlsv1.2 "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"
cd "$tmp"
awk -v asset="$asset" '$2==asset {print}' SHA256SUMS > selected.sha256
test -s selected.sha256
if command -v sha256sum >/dev/null 2>&1; then sha256sum -c selected.sha256; else shasum -a 256 -c selected.sha256; fi
mkdir bundle
tar -xzf "$asset" -C bundle
sh bundle/scripts/install.sh
