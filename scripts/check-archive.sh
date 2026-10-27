#!/bin/sh
# Sanity-check a release tarball: every entry appears exactly once, none is a hard link, the layout is the
# expected one and it extracts cleanly with the local tar. Usage: sh scripts/check-archive.sh FILE.tar.gz
set -eu
f="$1"
base=$(basename "$f" .tar.gz)
list=$(tar -tzf "$f")
dups=$(printf '%s\n' "$list" | sort | uniq -d)
[ -z "$dups" ] || { echo "check-archive: duplicate entries in $f:" >&2; printf '%s\n' "$dups" >&2; exit 1; }
# Only regular files and the one directory; no links of any kind (GNU lists them with an 'h' or 'l' type).
if tar -tvzf "$f" | cut -c1 | grep -qE '^[hl]'; then echo "check-archive: $f contains links" >&2; exit 1; fi
want="$base/ $base/LICENSE $base/classycash $base/classycash.openrc $base/env.example"
got=$(printf '%s\n' "$list" | LC_ALL=C sort | tr '\n' ' ' | sed 's/ $//')
want=$(printf '%s\n' $want | LC_ALL=C sort | tr '\n' ' ' | sed 's/ $//')
[ "$got" = "$want" ] || { echo "check-archive: unexpected contents of $f: $got" >&2; exit 1; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
tar -xzf "$f" -C "$tmp"
[ -x "$tmp/$base/classycash" ] || { echo "check-archive: binary missing or not executable" >&2; exit 1; }
echo "check-archive: $(basename "$f") ok"
