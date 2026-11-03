import { readFile, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { isVersion } from "./semver.mjs";

const version = process.argv[2];
if (!version || !isVersion(version)) {
  throw new Error(
    "Usage: node scripts/set-package-version.mjs <semver-version>",
  );
}

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
for (const packageName of ["browser", "language"]) {
  const manifestPath = join(root, "packages", packageName, "package.json");
  const contents = await readFile(manifestPath, "utf8");
  const updated = contents.replace(
    /("version":\s*")[^"]*(")/,
    `$1${version}$2`,
  );
  if (updated === contents || JSON.parse(updated).version !== version) {
    throw new Error(`Could not update the version in ${manifestPath}.`);
  }
  await writeFile(manifestPath, updated);
  process.stdout.write(`Set ${manifestPath} to ${version}.\n`);
}
