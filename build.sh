#!/usr/bin/env bash
# Cross-compile static zert binaries (CGO disabled) into dist/.
set -euo pipefail
cd "$(dirname "$0")"

OUT=dist
VERSION=${VERSION:-$(git describe --tags --always 2>/dev/null || echo dev)}
rm -rf "$OUT"
mkdir -p "$OUT"

for goos in linux darwin windows; do
  for goarch in amd64 arm64; do
    ext=""
    [ "$goos" = windows ] && ext=".exe"
    name="zert-$VERSION-$goos-$goarch$ext"
    CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
      go build -trimpath -ldflags="-s -w" -o "$OUT/$name" .
    echo "built $OUT/$name"
  done
done

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$OUT" && sha256sum -- * > SHA256SUMS)
elif command -v shasum >/dev/null 2>&1; then
  (cd "$OUT" && shasum -a 256 -- * > SHA256SUMS)
else
  echo "no sha256 tool found; skipping SHA256SUMS" >&2
  exit 1
fi
echo "wrote $OUT/SHA256SUMS"
