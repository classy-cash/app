#!/bin/sh
# Build reproducible release artifacts into dist/:
#   classycash_<version>_linux_<arch>.tar.gz   (binary, LICENSE, env.example, openrc script)
#   SHA256SUMS                                  (verify with: sha256sum -c SHA256SUMS)
# Usage: VERSION=1.2.3 sh scripts/release.sh
set -eu
VERSION="${VERSION:?set VERSION}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
DATE="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
PKG=github.com/classy-cash/app/internal/version
OUT="${OUT:-dist}"
rm -rf "$OUT" && mkdir -p "$OUT"
for ARCH in amd64 arm64; do
  NAME="classycash_${VERSION}_linux_${ARCH}"
  STAGE="$OUT/$NAME"
  mkdir -p "$STAGE"
  CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath \
    -ldflags "-s -w -X $PKG.Version=$VERSION -X $PKG.Commit=$COMMIT -X $PKG.Date=$DATE" \
    -o "$STAGE/classycash" ./cmd/classycash
  cp LICENSE deploy/env.example "$STAGE/"
  cp deploy/openrc/classycash "$STAGE/classycash.openrc"
  # Fixed mtime/owner keep the archive byte-for-byte reproducible.
  tar --sort=name --mtime='2026-01-01 00:00:00' --owner=0 --group=0 --numeric-owner -C "$OUT" -czf "$OUT/$NAME.tar.gz" "$NAME" 2>/dev/null \
    || tar -C "$OUT" -czf "$OUT/$NAME.tar.gz" "$NAME"
  rm -rf "$STAGE"
done
(cd "$OUT" && { sha256sum ./*.tar.gz 2>/dev/null || shasum -a 256 ./*.tar.gz; } | sed 's| \./| |' > SHA256SUMS)
echo "artifacts in $OUT/:"; ls -1 "$OUT"
