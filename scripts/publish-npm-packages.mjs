import { createHash } from "node:crypto";
import { readFile, readdir } from "node:fs/promises";
import { execFileSync } from "node:child_process";
import { basename, join, resolve } from "node:path";

const [packageDirectoryArgument, version] = process.argv.slice(2);
if (!packageDirectoryArgument || !version) {
  throw new Error("Usage: node scripts/publish-npm-packages.mjs <package-directory> <version>");
}

const packageDirectory = resolve(packageDirectoryArgument);
const packages = ["browser", "language"];
const tarballNames = packages.map((name) => `manifest-configuration-language-${name}-${version}.tgz`);
const expectedFiles = [...tarballNames, "SHA512SUMS"].sort();
const actualFiles = (await readdir(packageDirectory)).sort();
if (JSON.stringify(actualFiles) !== JSON.stringify(expectedFiles)) {
  throw new Error(`Unexpected npm tarball files: ${actualFiles.join(", ")}`);
}

const checksumLines = (await readFile(join(packageDirectory, "SHA512SUMS"), "utf8")).trim().split("\n");
const checksums = new Map();
for (const line of checksumLines) {
  const match = line.match(/^([a-f0-9]{128})  (.+)$/);
  if (!match || checksums.has(match[2])) {
    throw new Error(`Invalid SHA512SUMS entry: ${line}`);
  }
  checksums.set(match[2], match[1]);
}
if (checksums.size !== tarballNames.length || tarballNames.some((name) => !checksums.has(name))) {
  throw new Error("SHA512SUMS does not list exactly the expected npm tarballs.");
}

const packagesToPublish = [];
for (const [index, packageName] of packages.entries()) {
  const tarballName = tarballNames[index];
  const tarballPath = join(packageDirectory, tarballName);
  const tarball = await readFile(tarballPath);
  const sha512 = createHash("sha512").update(tarball).digest();
  const checksum = sha512.toString("hex");
  if (checksum !== checksums.get(tarballName)) {
    throw new Error(`SHA512 mismatch for ${tarballName}.`);
  }

  const expectedIntegrity = `sha512-${sha512.toString("base64")}`;
  const packageSpec = `@manifest-configuration-language/${packageName}@${version}`;
  let registryIntegrity;
  try {
    registryIntegrity = execFileSync("npm", ["view", packageSpec, "dist.integrity"], { encoding: "utf8" }).trim();
  } catch (error) {
    const details = `${error.stderr ?? ""}\n${error.stdout ?? ""}`;
    if (/\bE404\b|404 Not Found/.test(details)) {
      packagesToPublish.push(tarballPath);
      continue;
    }
    throw new Error(`Could not verify npm package ${packageSpec}: ${details.trim()}`, { cause: error });
  }

  if (registryIntegrity !== expectedIntegrity) {
    throw new Error(`Published ${packageSpec} does not match this run's tarball (expected ${expectedIntegrity}, found ${registryIntegrity}).`);
  }
  process.stdout.write(`Verified existing ${packageSpec} against this run tarball.\n`);
}

for (const tarballPath of packagesToPublish) {
  process.stdout.write(`Publishing ${basename(tarballPath)} with provenance.\n`);
  execFileSync("npm", ["publish", tarballPath, "--access", "public", "--provenance"], { stdio: "inherit" });
}
