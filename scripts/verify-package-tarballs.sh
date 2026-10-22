#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary_directory="$(mktemp -d)"
trap 'rm -rf "$temporary_directory"' EXIT

for package in browser language; do
  package_directory="$root/packages/$package"
  pack_json="$temporary_directory/$package-pack.json"
  (
    cd "$package_directory"
    npm pack --ignore-scripts --pack-destination "$temporary_directory" --json > "$pack_json"
  )
  archive="$(node -e 'const fs = require("node:fs"); process.stdout.write(JSON.parse(fs.readFileSync(process.argv[1], "utf8"))[0].filename)' "$pack_json")"
  printf '%s\n' "$temporary_directory/$archive" >> "$temporary_directory/archives.txt"
done

browser_archive="$(sed -n '1p' "$temporary_directory/archives.txt")"
language_archive="$(sed -n '2p' "$temporary_directory/archives.txt")"
project_directory="$temporary_directory/project"
mkdir -p "$project_directory"
cat > "$project_directory/package.json" <<EOF
{"private":true,"dependencies":{"@mcl/browser":"file:$browser_archive","@mcl/language":"file:$language_archive"}}
EOF
npm install --prefix "$project_directory" --ignore-scripts --no-audit --no-fund
cp "$root/scripts/verify-installed-packages.mjs" "$project_directory/verify-installed-packages.mjs"
(cd "$project_directory" && node verify-installed-packages.mjs)
MCL_BROWSER_PACKAGE="$project_directory/node_modules/@mcl/browser" \
  npm run --prefix "$root/packages/browser" test:browser:installed
