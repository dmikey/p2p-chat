#!/bin/sh
set -eu
version=${1:-v0.1.0}
platform=${2:-$(go env GOOS)}
arch=${3:-$(go env GOARCH)}
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT HUP INT TERM
extension=''; if [ "$platform" = windows ]; then extension=.exe; fi
CGO_ENABLED=0 GOOS="$platform" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$stage/radchat$extension" ./cmd/radchat
cp README.md LICENSE "$stage/"
cp -R deploy scripts "$stage/"
mkdir -p output
if [ "$platform" = windows ]; then
 (cd "$stage" && zip -qr "$root/output/radchat-${version}-${platform}-${arch}.zip" .)
else
 tar -czf "output/radchat-${version}-${platform}-${arch}.tar.gz" -C "$stage" .
fi
