#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary_directory="$(mktemp -d)"
trap 'rm -rf "$temporary_directory"' EXIT
output_directory="${1:-$temporary_directory/packages}"
mkdir -p "$output_directory"
output_directory="$(cd "$output_directory" && pwd)"
for package in browser language; do
  version="$(node -p "require('$root/packages/$package/package.json').version")"
  if [[ -n "${MCL_VERSION:-}" && "$version" != "$MCL_VERSION" ]]; then
    printf 'Package @mcl/%s version %s does not match expected MCL version %s.\n' \
      "$package" "$version" "$MCL_VERSION" >&2
    exit 1
  fi
  rm -f "$output_directory/mcl-$package-$version.tgz"
done
rm -f "$output_directory/SHA512SUMS"

for package in browser language; do
  source_directory="$root/packages/$package"
  pack_json="$temporary_directory/$package-pack.json"
  (
    cd "$source_directory"
    npm pack --pack-destination "$output_directory" --json > "$pack_json"
  )
  archive="$(node -e 'const fs = require("node:fs"); process.stdout.write(JSON.parse(fs.readFileSync(process.argv[1], "utf8"))[0].filename)' "$pack_json")"
  case "$package" in
    browser) required_files=(dist/mcl.wasm dist/wasm_exec.js) ;;
    language) required_files=(src/parser.js src/mcl.tmLanguage.json) ;;
  esac
  node - "$pack_json" "${required_files[@]}" <<'NODE'
const fs = require('node:fs');
const [packJson, ...required] = process.argv.slice(2);
const packed = JSON.parse(fs.readFileSync(packJson, 'utf8'))[0];
const files = packed.files.map(({ path }) => path);
const missing = required.filter((path) => !files.includes(path));
if (missing.length) throw new Error(`Package archive is missing: ${missing.join(', ')}`);
NODE
  printf '%s\n' "$output_directory/$archive" >> "$temporary_directory/archives.txt"
done

browser_archive="$(sed -n '1p' "$temporary_directory/archives.txt")"
language_archive="$(sed -n '2p' "$temporary_directory/archives.txt")"
project_directory="$temporary_directory/project"
mkdir -p "$project_directory"
cat > "$project_directory/package.json" <<EOF
{"private":true,"type":"module","dependencies":{"@mcl/browser":"file:$browser_archive","@mcl/language":"file:$language_archive"}}
EOF
npm install --prefix "$project_directory" --ignore-scripts --no-audit --no-fund
cp "$root/scripts/verify-installed-packages.mjs" "$project_directory/verify-installed-packages.mjs"
(cd "$project_directory" && MCL_BROWSER_TEST_PACKAGE="$root/packages/browser" node verify-installed-packages.mjs)

consumer_fixture="$root/scripts/package-consumer-smoke"
cp "$consumer_fixture/index.html" "$project_directory/index.html"
cp "$consumer_fixture/vite.config.js" "$project_directory/vite.config.js"
mkdir -p "$project_directory/src"
cp "$consumer_fixture/main.js" "$project_directory/src/main.js"
node "$consumer_fixture/run.mjs" "$project_directory" "$root/packages/browser"
(
  cd "$output_directory"
  expected_archives="$(printf '%s\n' "$(basename "$browser_archive")" "$(basename "$language_archive")" | sort)"
  actual_archives="$(for archive in ./*.tgz; do [ -f "$archive" ] && basename "$archive"; done | sort)"
  if [ "$actual_archives" != "$expected_archives" ]; then
    printf 'Unexpected npm package archives: %s\n' "$actual_archives" >&2
    exit 1
  fi
  sha512sum -- *.tgz > SHA512SUMS
)
