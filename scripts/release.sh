#!/bin/sh
# Build reproducible release artifacts into dist/:
#   classycash_<version>_linux_<arch>.tar.gz   (binary, LICENSE, env.example, openrc script)
#   SHA256SUMS                                  (verify with: sha256sum -c SHA256SUMS)
# Usage: sh scripts/release.sh            (version = current UTC time, e.g. 202610271430)
#        VERSION=202610271430 sh scripts/release.sh
set -eu
# Legacy scheme: UTC build time YYYYMMDDHHMM. Pass VERSION to rebuild an existing release byte-for-byte.
VERSION="${VERSION:-$(date -u +%Y%m%d%H%M)}"
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
  # Deterministic archive: fixed mtimes, sorted entries, numeric root ownership, no gzip timestamp.
  # --no-recursion is essential: the list already names the directory AND every file in it, and without it
  # GNU tar archives each file twice (the second time as a hard link to itself, which breaks bsdtar).
  find "$STAGE" -exec touch -t 202601010000 {} +
  if tar --version 2>/dev/null | grep -q GNU; then OWNER="--owner=0 --group=0 --numeric-owner"; else OWNER="--uid 0 --gid 0 --numeric-owner"; fi
  (cd "$OUT" && find "$NAME" | LC_ALL=C sort | tar $OWNER --no-recursion -cf - -T -) | gzip -n -9 > "$OUT/$NAME.tar.gz"
  rm -rf "$STAGE"
  sh "$(dirname "$0")/check-archive.sh" "$OUT/$NAME.tar.gz"
done
(cd "$OUT" && { sha256sum ./*.tar.gz 2>/dev/null || shasum -a 256 ./*.tar.gz; } | sed 's| \./| |' > SHA256SUMS)
echo "artifacts in $OUT/:"; ls -1 "$OUT"
