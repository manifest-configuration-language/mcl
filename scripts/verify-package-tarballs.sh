#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_directory="$1"
mkdir -p "$output_directory"
output_directory="$(cd "$output_directory" && pwd)"
for package in browser language; do
  source_directory="$root/packages/$package"
  version="$(node -p "require('$source_directory/package.json').version")"
  if [[ -n "${MCL_VERSION:-}" && "$version" != "$MCL_VERSION" ]]; then
    printf 'Package @manifest-configuration-language/%s version %s does not match expected MCL version %s.\n' \
      "$package" "$version" "$MCL_VERSION" >&2
    exit 1
  fi
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
  fi
done

cd "$output_directory"
for archive in ./*.tgz; do
  echo "Validating $archive ..."
  pnpm --dir "$root/packages/browser" exec publint --strict "$archive"
  pnpm --dir "$root/packages/browser" exec attw --ignore-missing-peer "$archive"
done
