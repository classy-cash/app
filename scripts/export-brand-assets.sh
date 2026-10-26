#!/bin/sh
set -eu

brand_root=$(CDPATH= cd -- "$(dirname -- "$0")/../assets/brand" && pwd)
command -v rsvg-convert >/dev/null 2>&1 || {
  echo 'rsvg-convert is required to export the SVG brand sources.' >&2
  exit 1
}

for brand_size in 16 32 180 192 512 1024; do
  rsvg-convert --width "$brand_size" --height "$brand_size" \
    --output "$brand_root/icon-$brand_size.png" "$brand_root/icon.svg"
done

rsvg-convert --width 920 --output "$brand_root/logo.png" "$brand_root/logo.svg"
rsvg-convert --width 920 --output "$brand_root/logo-on-dark.png" "$brand_root/logo-on-dark.svg"
rsvg-convert --output "$brand_root/social-card.png" "$brand_root/social-card.svg"
rsvg-convert --width 1280 --page-width 1280 --page-height 640 \
  --output "$brand_root/repository-cover.png" "$brand_root/social-card.svg"
