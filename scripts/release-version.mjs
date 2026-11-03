import { execFileSync } from "node:child_process";
import {
  bumpVersion,
  compareVersions,
  isVersion,
  parseVersion,
} from "./semver.mjs";

function readArguments(argv) {
  const options = { bump: "patch", releaseType: "latest", version: null };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    const value = argv[index + 1];
    if (argument === "--bump") {
      options.bump = value;
      index += 1;
    } else if (argument === "--release-type") {
      options.releaseType = value;
      index += 1;
    } else if (argument === "--version") {
      options.version = value;
      index += 1;
    } else {
      throw new Error(`Unknown argument: ${argument}`);
    }
  }
  if (!["patch", "minor", "major"].includes(options.bump)) {
    throw new Error(`Invalid version bump: ${options.bump}`);
  }
  if (!["none", "pre-release", "latest"].includes(options.releaseType)) {
    throw new Error(`Invalid release type: ${options.releaseType}`);
  }
  return options;
}

function readTags() {
  const output = execFileSync("git", ["tag", "--list", "v*"], {
    encoding: "utf8",
  });
  return output
    .split("\n")
    .map((tag) => tag.trim())
    .filter((tag) => tag.startsWith("v") && isVersion(tag.slice(1)))
    .map((tag) => tag.slice(1));
}

function nextPrereleaseNumber(versions, base) {
  const baseVersion = parseVersion(base);
  const numbers = versions
    .map((version) => parseVersion(version))
    .filter(
      (parsed) =>
        parsed &&
        parsed.major === baseVersion.major &&
        parsed.minor === baseVersion.minor &&
        parsed.patch === baseVersion.patch &&
        parsed.prerelease.length === 2 &&
        parsed.prerelease[0] === "rc" &&
        /^\d+$/.test(parsed.prerelease[1]),
    )
    .map((parsed) => Number(parsed.prerelease[1]));
  return numbers.length === 0 ? 1 : Math.max(...numbers) + 1;
}

const options = readArguments(process.argv.slice(2));
const versions = readTags();
const stableVersions = versions
  .filter((version) => parseVersion(version).prerelease.length === 0)
  .sort(compareVersions);

let version;
if (options.version) {
  version = options.version.startsWith("v")
    ? options.version.slice(1)
    : options.version;
  if (!isVersion(version)) {
    throw new Error(`Invalid release version: ${options.version}`);
  }
  const parsed = parseVersion(version);
  if (options.releaseType === "latest" && parsed.prerelease.length > 0) {
    throw new Error(
      `Release type latest requires a stable version, received ${version}.`,
    );
  }
  if (options.releaseType === "pre-release" && parsed.prerelease.length === 0) {
    throw new Error(
      `Release type pre-release requires a prerelease version, received ${version}.`,
    );
  }
} else {
  const latestStable = stableVersions.at(-1) ?? "0.0.0";
  const nextStable = bumpVersion(latestStable, options.bump);
  version =
    options.releaseType === "pre-release"
      ? `${nextStable}-rc.${nextPrereleaseNumber(versions, nextStable)}`
      : nextStable;
}

process.stdout.write(
  `${JSON.stringify({ version, tag: `v${version}` })}\n`,
);
