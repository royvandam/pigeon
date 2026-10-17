#!/usr/bin/env bash
# Copy the Font Awesome Free files the pages use from the pinned npm release
# into web/static/fontawesome, verifying the package checksum first. CI runs
# this script and fails if the committed files differ. To upgrade, change the
# version and checksum, rerun it, and check the icons still exist.
set -euo pipefail

version=7.1.0
# SHA-256 of the npm tarball, which matches the registry's published sha512
# integrity value.
sha256=566426b219fdb2a703ae9388b13add58274a4867df154b1d950de511f089bfde

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
target="$root/web/static/fontawesome"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

curl -fsSL --retry 3 -o "$work/package.tgz" \
  "https://registry.npmjs.org/@fortawesome/fontawesome-free/-/fontawesome-free-$version.tgz"
if command -v sha256sum >/dev/null; then
  actual="$(sha256sum "$work/package.tgz" | cut -d' ' -f1)"
else
  actual="$(shasum -a 256 "$work/package.tgz" | cut -d' ' -f1)"
fi
if [ "$actual" != "$sha256" ]; then
  echo "vendor-fontawesome.sh: checksum mismatch for Font Awesome Free $version" >&2
  exit 1
fi

tar -xzf "$work/package.tgz" -C "$work" \
  package/LICENSE.txt \
  package/css/fontawesome.min.css \
  package/css/solid.min.css \
  package/webfonts/fa-solid-900.woff2

rm -rf "$target"
mkdir -p "$target/css" "$target/webfonts"
install -m 0644 "$work/package/LICENSE.txt" "$target/LICENSE.txt"
install -m 0644 "$work/package/css/fontawesome.min.css" "$work/package/css/solid.min.css" "$target/css/"
install -m 0644 "$work/package/webfonts/fa-solid-900.woff2" "$target/webfonts/"
