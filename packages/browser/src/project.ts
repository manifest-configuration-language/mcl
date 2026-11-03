import {
  maxProjectBytes,
  maxProjectFileBytes,
  maxProjectFiles,
  maxProjectPathDepth,
  maxProjectPathLength,
  maxSourceBytes,
} from "./limits.js";
import type { Request as WasmRequest } from "./protocol.js";

export type MclOperation = WasmRequest["operation"];

export {
  maxProjectBytes,
  maxProjectFileBytes,
  maxProjectFiles,
  maxProjectPathDepth,
  maxProjectPathLength,
  maxSourceBytes,
};

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

/** Root-relative project files passed to MCL's in-memory session filesystem.
 *  Uint8Array values are copied before transfer so callers can reuse them. */
export type ProjectFile = string | Uint8Array;
export type ProjectFiles = Readonly<Record<string, ProjectFile>>;

// ---------------------------------------------------------------------------
// Path validation helpers (exported for callers such as website)
// ---------------------------------------------------------------------------

export function isValidProjectPath(value: unknown): value is string {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.startsWith("/") ||
    /^[a-z]:/i.test(value) ||
    value.includes("\\") ||
    value.includes("\0")
  ) {
    return false;
  }
  let pathLength = 0;
  let pathDepth = 1;
  for (const character of value) {
    if (++pathLength > maxProjectPathLength) return false;
    if (character === "/" && ++pathDepth > maxProjectPathDepth) return false;
  }
  return value.split("/").every((part) => part !== "" && part !== "." && part !== "..");
}

export function isValidProjectFileSet(paths: readonly string[]): boolean {
  if (!Array.isArray(paths)) return false;
  const files = new Set<string>();
  const directories = new Set<string>();
  for (const filePath of paths) {
    if (!isValidProjectPath(filePath) || files.has(filePath) || directories.has(filePath))
      return false;
    let separator = filePath.indexOf("/");
    while (separator !== -1) {
      const directory = filePath.slice(0, separator);
      if (files.has(directory)) return false;
      directories.add(directory);
      separator = filePath.indexOf("/", separator + 1);
    }
    files.add(filePath);
  }
  return true;
}

// ---------------------------------------------------------------------------
// Snapshot internals
// ---------------------------------------------------------------------------

export interface ProjectSnapshot {
  readonly files: Readonly<Record<string, ProjectFile>>;
  readonly sizes: Readonly<Record<string, number>>;
  readonly defaultEntrypoint: string | undefined;
}

function sameContent(left: ProjectFile, right: ProjectFile): boolean {
  if (left === right) return true;
  if (typeof left !== typeof right) return false;
  if (typeof left === "string") return left === right;
  const l = left as Uint8Array;
  const r = right as Uint8Array;
  if (l.byteLength !== r.byteLength) return false;
  for (let index = 0; index < l.byteLength; index++) {
    if (l[index] !== r[index]) return false;
  }
  return true;
}

export function createProjectSnapshot(
  files: ProjectFiles,
  previous?: ProjectSnapshot,
): ProjectSnapshot {
  const entries = Object.entries(files);
  if (entries.length === 0)
    throw new TypeError("Project must contain at least one MCL source file");
  if (entries.length > maxProjectFiles)
    throw new RangeError(`Project has more than ${maxProjectFiles} files`);
  if (!isValidProjectFileSet(entries.map(([filePath]) => filePath)))
    throw new TypeError(
      "Project file paths must be valid and cannot conflict with a file or directory path",
    );
  let totalBytes = 0;
  const projectFiles: Record<string, ProjectFile> = Object.create(null);
  const fileSizes: Record<string, number> = Object.create(null);
  for (const [filePath, input] of entries) {
    if (typeof input !== "string" && !(input instanceof Uint8Array))
      throw new TypeError(`Project file ${filePath} must be a string or Uint8Array`);
    const previousContent = previous?.files[filePath];
    const unchanged = previousContent !== undefined && sameContent(previousContent, input);
    const content = unchanged
      ? previousContent!
      : typeof input === "string"
        ? input
        : new Uint8Array(input);
    const fileBytes = unchanged
      ? previous!.sizes[filePath]!
      : typeof content === "string"
        ? new TextEncoder().encode(content).byteLength
        : (content as Uint8Array).byteLength;
    if (!unchanged && fileBytes > maxProjectFileBytes)
      throw new RangeError(
        `Project file ${filePath} exceeds the ${maxProjectFileBytes}-byte limit`,
      );
    totalBytes += fileBytes;
    if (totalBytes > maxProjectBytes)
      throw new RangeError(`Project files exceed the ${maxProjectBytes}-byte limit`);
    projectFiles[filePath] = content;
    fileSizes[filePath] = fileBytes!;
  }
  const sourcePaths = Object.keys(projectFiles)
    .filter((filePath) => filePath.endsWith(".mcl"))
    .sort();
  if (sourcePaths.length === 0)
    throw new TypeError("Project must contain at least one MCL source file");
  const defaultEntrypoint = Object.hasOwn(projectFiles, "main.mcl")
    ? "main.mcl"
    : sourcePaths.length === 1
      ? sourcePaths[0]
      : undefined;
  return Object.freeze({
    files: Object.freeze(projectFiles),
    sizes: Object.freeze(fileSizes),
    defaultEntrypoint,
  });
}

// ---------------------------------------------------------------------------
// ProjectDelta for Worker sync
// ---------------------------------------------------------------------------

export interface FileDelta {
  encoding: "utf8" | "binary";
  content?: string;
}

export interface ProjectDelta {
  reset: boolean;
  files: Record<string, FileDelta>;
  deleted: string[];
}

/**
 * Compute the minimal delta between `previous` and `next` snapshots.
 * Returns `null` if there is nothing to send (no changes).
 */
export function computeProjectDelta(
  next: ProjectSnapshot,
  previous: ProjectSnapshot | undefined,
  reset: boolean,
): {
  delta: ProjectDelta;
  binaryFiles: Record<string, Uint8Array>;
  transferables: ArrayBuffer[];
} | null {
  const previousFiles =
    reset || !previous ? (Object.create(null) as Record<string, ProjectFile>) : previous.files;
  const updates: Record<string, FileDelta> = Object.create(null);
  const binaryFiles: Record<string, Uint8Array> = Object.create(null);
  const transferables: ArrayBuffer[] = [];
  const deleted: string[] = [];

  for (const [filePath, content] of Object.entries(next.files)) {
    if (
      !reset &&
      previous &&
      Object.hasOwn(previousFiles, filePath) &&
      sameContent(previousFiles[filePath]!, content)
    )
      continue;
    if (typeof content === "string") {
      updates[filePath] = { encoding: "utf8", content };
    } else {
      const copy = new Uint8Array(content as Uint8Array);
      updates[filePath] = { encoding: "binary" };
      binaryFiles[filePath] = copy;
      transferables.push(copy.buffer as ArrayBuffer);
    }
  }

  if (!reset && previous) {
    for (const filePath of Object.keys(previous.files)) {
      if (!Object.hasOwn(next.files, filePath)) deleted.push(filePath);
    }
  }

  if (!reset && Object.keys(updates).length === 0 && deleted.length === 0) return null;

  return {
    delta: { reset, files: updates, deleted },
    binaryFiles,
    transferables,
  };
}

// ---------------------------------------------------------------------------
// Operation metadata
// ---------------------------------------------------------------------------

export const operationPriorities: Record<string, number> = {
  render: 30,
  evaluate: 30,
  check: 30,
  parse: 20,
  format: 20,
  rename: 20,
  renameFileImports: 20,
  references: 20,
  definition: 20,
  completion: 20,
  hover: 20,
  lint: 10,
};

export const latestOnlyOperations = new Set<string>(["completion", "hover", "lint"]);
export const requestPriorities = { interactive: 40, background: 0 } as const;

// ---------------------------------------------------------------------------
// Request shape helpers
// ---------------------------------------------------------------------------

export interface RequestOptions {
  signal?: AbortSignal;
  timeoutMs?: number;
  priority?: "interactive" | "background";
}

export interface ProjectOperationOptions extends RequestOptions {
  entrypoint?: string;
  parameters?: Record<string, string>;
}

export interface PositionOptions {
  line: number;
  character: number;
}

export interface ProjectCompletionOptions extends ProjectOperationOptions {
  position: PositionOptions;
}

export interface ProjectNavigationOptions extends ProjectOperationOptions {
  position: PositionOptions;
}

export interface ProjectRenameOptions extends ProjectNavigationOptions {
  newName: string;
}

export interface ProjectFileRenameOptions extends ProjectOperationOptions {
  oldPath: string;
  newPath: string;
}
