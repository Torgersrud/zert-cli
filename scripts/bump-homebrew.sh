#!/usr/bin/env bash
# Bump the zert formula in the homebrew-zert tap to a released tag.
# usage: scripts/bump-homebrew.sh vX.Y.Z [path/to/homebrew-zert]
set -euo pipefail

VERSION=${1:?usage: bump-homebrew.sh vX.Y.Z [tap-dir]}
TAP_DIR=${2:-../homebrew-zert}
FORMULA="$TAP_DIR/Formula/zert.rb"
REPO=${REPO:-Torgersrud/zert-cli}
URL="https://github.com/$REPO/archive/refs/tags/$VERSION.tar.gz"

[ -f "$FORMULA" ] || { echo "formula not found: $FORMULA" >&2; exit 1; }

if command -v sha256sum >/dev/null 2>&1; then
  SHA=$(curl -fsSL "$URL" | sha256sum | cut -d' ' -f1)
else
  SHA=$(curl -fsSL "$URL" | shasum -a 256 | cut -d' ' -f1)
fi

sed -i.bak -E "s|^  url \"[^\"]*\"|  url \"$URL\"|; s|^  sha256 \"[^\"]*\"|  sha256 \"$SHA\"|" "$FORMULA"
rm -f "$FORMULA.bak"
echo "updated $FORMULA to $VERSION ($SHA)"

if git -C "$TAP_DIR" rev-parse --git-dir >/dev/null 2>&1; then
  git -C "$TAP_DIR" commit -aqm "zert $VERSION"
  git -C "$TAP_DIR" push
  echo "pushed bump commit"
else
  echo "$TAP_DIR is not a git repo; skipped commit/push"
fi
