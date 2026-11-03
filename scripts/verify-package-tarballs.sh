#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary_directory="$(mktemp -d)"
trap 'rm -rf "$temporary_directory"' EXIT
output_directory="$temporary_directory/packages"
for argument in "$@"; do
  output_directory="$argument"
done
mkdir -p "$output_directory"
output_directory="$(cd "$output_directory" && pwd)"
for package in browser language; do
  version="$(node -p "require('$root/packages/$package/package.json').version")"
  if [[ -n "${MCL_VERSION:-}" && "$version" != "$MCL_VERSION" ]]; then
    printf 'Package @manifest-configuration-language/%s version %s does not match expected MCL version %s.\n' \
      "$package" "$version" "$MCL_VERSION" >&2
    exit 1
  fi
  rm -f "$output_directory/manifest-configuration-language-$package-$version.tgz"
done

for package in browser language; do
  source_directory="$root/packages/$package"
  package_name="$(node -p "require('$source_directory/package.json').name")"
  package_version="$(node -p "require('$source_directory/package.json').version")"
  archive_name="${package_name#@}"
  archive_name="${archive_name//\//-}-$package_version.tgz"
  (
    cd "$source_directory"
    MCL_BROWSER_WASM_PREBUILT=true pnpm pack --pack-destination "$output_directory" >/dev/null
  )
  archive="$archive_name"
  if [[ "$package" == browser ]]; then
    if [[ ! -f "$root/.build/release-assets/mcl.wasm.gz" ]]; then
      printf 'The staged GitHub Release WASM is missing.\n' >&2
      exit 1
    fi
    tar -xOf "$output_directory/$archive" package/dist/mcl.wasm | cmp - "$root/packages/browser/dist/mcl.wasm"
    gzip -dc "$root/.build/release-assets/mcl.wasm.gz" | cmp - "$root/packages/browser/dist/mcl.wasm"
  fi
  printf '%s\n' "$output_directory/$archive" >> "$temporary_directory/archives.txt"
done

(
  cd "$output_directory"
  if [ -f SHA512SUMS ]; then
    sha512sum --check SHA512SUMS
  else
    sha512sum -- *.tgz > SHA512SUMS
  fi
)
