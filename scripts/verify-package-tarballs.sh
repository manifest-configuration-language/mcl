#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary_directory="$(mktemp -d)"
reference_package="$root/packages/reference"
trap 'rm -rf "$temporary_directory" "$reference_package/reference"' EXIT
output_directory="$temporary_directory/packages"
prebuilt_browser_wasm=false
for argument in "$@"; do
  case "$argument" in
    --prebuilt-browser-wasm) prebuilt_browser_wasm=true ;;
    *) output_directory="$argument" ;;
  esac
done
mkdir -p "$output_directory"
output_directory="$(cd "$output_directory" && pwd)"
for package in browser language reference; do
  version="$(node -p "require('$root/packages/$package/package.json').version")"
  if [[ -n "${MCL_VERSION:-}" && "$version" != "$MCL_VERSION" ]]; then
    printf 'Package @manifest-configuration-language/%s version %s does not match expected MCL version %s.\n' \
      "$package" "$version" "$MCL_VERSION" >&2
    exit 1
  fi
  rm -f "$output_directory/manifest-configuration-language-$package-$version.tgz"
done
rm -f "$output_directory/SHA512SUMS"

for package in browser language; do
  source_directory="$root/packages/$package"
  package_name="$(node -p "require('$source_directory/package.json').name")"
  package_version="$(node -p "require('$source_directory/package.json').version")"
  archive_name="${package_name#@}"
  archive_name="${archive_name//\//-}-$package_version.tgz"
  (
    cd "$source_directory"
    if [[ "$package" == browser && "$prebuilt_browser_wasm" == true ]]; then
      MCL_BROWSER_WASM_PREBUILT=true pnpm pack --pack-destination "$output_directory" >/dev/null
    else
      pnpm pack --pack-destination "$output_directory" >/dev/null
    fi
  )
  archive="$archive_name"
  if [[ "$package" == browser && "$prebuilt_browser_wasm" == true ]]; then
    if [[ ! -f "$root/.build/release-assets/mcl.wasm.gz" ]]; then
      printf 'The staged GitHub Release WASM is missing.\n' >&2
      exit 1
    fi
    tar -xOf "$output_directory/$archive" package/dist/mcl.wasm | cmp - "$root/packages/browser/dist/mcl.wasm"
    gzip -dc "$root/.build/release-assets/mcl.wasm.gz" | cmp - "$root/packages/browser/dist/mcl.wasm"
  fi
  printf '%s\n' "$output_directory/$archive" >> "$temporary_directory/archives.txt"
done

reference_directory="$root/.build/reference/reference"
if [[ ! -s "$reference_directory/cli/mcl.md" || ! -s "$reference_directory/stdlib-functions.md" ]]; then
  printf 'Generated MCL reference files are missing. Run mise run release:reference first.\n' >&2
  exit 1
fi
rm -rf "$reference_package/reference"
cp -R "$reference_directory" "$reference_package/reference"
reference_version="$(node -p "require('$reference_package/package.json').version")"
reference_archive_name="manifest-configuration-language-reference-$reference_version.tgz"
(
  cd "$reference_package"
  pnpm pack --pack-destination "$output_directory" >/dev/null
)
printf '%s\n' "$output_directory/$reference_archive_name" >> "$temporary_directory/archives.txt"

browser_archive="$(sed -n '1p' "$temporary_directory/archives.txt")"
language_archive="$(sed -n '2p' "$temporary_directory/archives.txt")"
reference_archive="$(sed -n '3p' "$temporary_directory/archives.txt")"
project_directory="$temporary_directory/project"
mkdir -p "$project_directory"
cat > "$project_directory/package.json" <<EOF
{"private":true,"type":"module","dependencies":{"@manifest-configuration-language/browser":"file:$browser_archive","@manifest-configuration-language/language":"file:$language_archive","@manifest-configuration-language/reference":"file:$reference_archive"}}
EOF
npm install --prefix "$project_directory" --ignore-scripts --no-audit --no-fund
cp "$root/scripts/verify-installed-packages.mjs" "$project_directory/verify-installed-packages.mjs"
(cd "$project_directory" && node verify-installed-packages.mjs)
(
  cd "$output_directory"
  expected_archives="$(printf '%s\n' "$(basename "$browser_archive")" "$(basename "$language_archive")" "$(basename "$reference_archive")" | sort)"
  actual_archives="$(for archive in ./*.tgz; do [ -f "$archive" ] && basename "$archive"; done | sort)"
  if [ "$actual_archives" != "$expected_archives" ]; then
    printf 'Unexpected npm package archives: %s\n' "$actual_archives" >&2
    exit 1
  fi
  sha512sum -- *.tgz > SHA512SUMS
)
