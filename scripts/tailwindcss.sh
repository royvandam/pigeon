#!/usr/bin/env bash
# Run the pinned Tailwind CSS standalone CLI, downloading and verifying it on
# first use. Local builds and CI use the same release, so generated CSS is
# reproducible. To upgrade, change the version and copy the checksums from the
# release's sha256sums.txt.
set -euo pipefail

version=4.3.3

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) asset=tailwindcss-linux-x64 sha256=dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a ;;
  Linux-aarch64 | Linux-arm64) asset=tailwindcss-linux-arm64 sha256=55fd0b241214eff3de1e8ee4f22796662f2d2e7a49bcfca7477cfd0bac398195 ;;
  Darwin-x86_64) asset=tailwindcss-macos-x64 sha256=7922e0953f2110c05976e3bf58f14e643d90427575e766b7d433f5f80cbee7e1 ;;
  Darwin-arm64) asset=tailwindcss-macos-arm64 sha256=cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d ;;
  *)
    echo "tailwindcss.sh: unsupported platform $(uname -s)-$(uname -m)" >&2
    exit 1
    ;;
esac

cache="${XDG_CACHE_HOME:-$HOME/.cache}/pigeon/tailwindcss-$version"
binary="$cache/$asset"

if [ ! -x "$binary" ]; then
  mkdir -p "$cache"
  download="$(mktemp "$cache/$asset.XXXXXX")"
  trap 'rm -f "$download"' EXIT
  curl -fsSL --retry 3 -o "$download" \
    "https://github.com/tailwindlabs/tailwindcss/releases/download/v$version/$asset"
  if command -v sha256sum >/dev/null; then
    actual="$(sha256sum "$download" | cut -d' ' -f1)"
  else
    actual="$(shasum -a 256 "$download" | cut -d' ' -f1)"
  fi
  if [ "$actual" != "$sha256" ]; then
    echo "tailwindcss.sh: checksum mismatch for $asset v$version" >&2
    exit 1
  fi
  chmod +x "$download"
  mv "$download" "$binary"
  trap - EXIT
fi

exec "$binary" "$@"
