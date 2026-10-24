import type {
  CheckResult,
  CompletionItem as ProtocolCompletionItem,
  CompletionPosition as ProtocolCompletionPosition,
  CompletionResult as ProtocolCompletionResult,
  DefinitionLocation as ProtocolDefinitionLocation,
  DefinitionResult as ProtocolDefinitionResult,
  Error as ProtocolError,
  EvaluateResult,
  FormatResult,
  HoverInfo as ProtocolHoverInfo,
  HoverResult as ProtocolHoverResult,
  LintDiagnostic as ProtocolLintDiagnostic,
  ParseResult,
  RelatedLocation as ProtocolRelatedLocation,
  RenderResult,
  Request as WasmRequest,
  SourceRange as ProtocolSourceRange,
} from "./protocol.js";

export type MclOperation = WasmRequest["operation"];

export declare const maxSourceBytes: number;
export declare const maxProjectFiles: number;
export declare const maxProjectFileBytes: number;
export declare const maxProjectBytes: number;
export declare const maxProjectPathLength: number;
export declare const maxProjectPathDepth: number;
export declare function isValidProjectPath(path: unknown): path is string;
export declare function isValidProjectFileSet(paths: readonly string[]): boolean;

/** Root-relative project files passed to MCL's in-memory session filesystem. Uint8Array values are copied before transfer so callers can reuse them. */
export type ProjectFile = string | Uint8Array;
export type ProjectFiles = Readonly<Record<string, ProjectFile>>;

export type MclError = ProtocolError;

export type LintDiagnostic = ProtocolLintDiagnostic;
export type CompletionPosition = ProtocolCompletionPosition;
export type CompletionItem = ProtocolCompletionItem;
export type CompletionResult = ProtocolCompletionResult;
export type DefinitionResult = ProtocolDefinitionResult;
export type DefinitionLocation = ProtocolDefinitionLocation;
export type HoverResult = ProtocolHoverResult;
export type HoverInfo = ProtocolHoverInfo;
export type SourceRange = ProtocolSourceRange;

export type MclResult =
  | ({ operation: "parse" } & ParseResult)
  | { operation: "lint"; diagnostics: ProtocolLintDiagnostic[] }
  | ({ operation: "completion" } & ProtocolCompletionResult)
  | ({ operation: "definition" } & ProtocolDefinitionResult)
  | ({ operation: "hover" } & ProtocolHoverResult)
  | ({ operation: "format" } & FormatResult)
  | ({ operation: "check" } & CheckResult)
  | ({ operation: "evaluate" } & EvaluateResult)
  | ({ operation: "render" } & RenderResult);

export interface RuntimeOptions {
  initializationTimeoutMs?: number;
  operationTimeoutMs?: number;
  workerFactory?: () => Worker;
}

export interface MclRuntime {
  openProject(files: ProjectFiles): MclProject;
  warmUp(options?: RequestOptions): Promise<void>;
  parse(source: string, options?: RequestOptions): Promise<MclResult & { operation: "parse" }>;
  lint(source: string, options?: RequestOptions): Promise<MclResult & { operation: "lint" }>;
  format(source: string, options?: RequestOptions): Promise<MclResult & { operation: "format" }>;
  check(
    source: string,
    files?: ProjectFiles,
    options?: RequestOptions,
  ): Promise<MclResult & { operation: "check" }>;
  evaluate(
    source: string,
    files?: ProjectFiles,
    options?: RequestOptions,
  ): Promise<MclResult & { operation: "evaluate" }>;
  render(
    source: string,
    files?: ProjectFiles,
    options?: RequestOptions,
  ): Promise<MclResult & { operation: "render" }>;
  definition(
    source: string,
    options: PositionRequestOptions,
  ): Promise<MclResult & { operation: "definition" }>;
  definition(
    source: string,
    files: ProjectFiles | undefined,
    options: PositionRequestOptions,
  ): Promise<MclResult & { operation: "definition" }>;
  hover(
    source: string,
    options: PositionRequestOptions,
  ): Promise<MclResult & { operation: "hover" }>;
  hover(
    source: string,
    files: ProjectFiles | undefined,
    options: PositionRequestOptions,
  ): Promise<MclResult & { operation: "hover" }>;
  dispose(): void;
}

export interface MclProject {
  /** Returns a new complete snapshot handle for this Worker session. Only changed files are sent on the next operation. */
  update(files: ProjectFiles): MclProject;
  parse(options?: ProjectOperationOptions): Promise<MclResult & { operation: "parse" }>;
  lint(options?: ProjectOperationOptions): Promise<MclResult & { operation: "lint" }>;
  complete(options: ProjectCompletionOptions): Promise<MclResult & { operation: "completion" }>;
  definition(options: ProjectNavigationOptions): Promise<MclResult & { operation: "definition" }>;
  hover(options: ProjectNavigationOptions): Promise<MclResult & { operation: "hover" }>;
  format(options?: ProjectOperationOptions): Promise<MclResult & { operation: "format" }>;
  check(options?: ProjectOperationOptions): Promise<MclResult & { operation: "check" }>;
  evaluate(options?: ProjectOperationOptions): Promise<MclResult & { operation: "evaluate" }>;
  render(options?: ProjectOperationOptions): Promise<MclResult & { operation: "render" }>;
}

export interface RequestOptions {
  signal?: AbortSignal;
  timeoutMs?: number;
}

export interface ProjectOperationOptions extends RequestOptions {
  entrypoint?: string;
}

export interface ProjectCompletionOptions extends ProjectOperationOptions {
  position: CompletionPosition;
}

export interface ProjectNavigationOptions extends ProjectOperationOptions {
  position: CompletionPosition;
}

export interface PositionRequestOptions extends RequestOptions {
  entrypoint?: string;
  position: CompletionPosition;
}

export declare class MclRuntimeError extends Error {
  readonly code: string;
  readonly path?: string;
  readonly line?: number;
  readonly column?: number;
  readonly endLine?: number;
  readonly endColumn?: number;
  readonly related?: ProtocolRelatedLocation[];
}

export declare function createMclRuntime(options?: RuntimeOptions): MclRuntime;
