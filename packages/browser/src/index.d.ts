import type {
  CheckResult,
  Error as ProtocolError,
  EvaluateResult,
  FormatResult,
  LintDiagnostic as ProtocolLintDiagnostic,
  ParseResult,
  RenderResult,
  Request as WasmRequest,
} from "./protocol.js";

export type MclOperation = WasmRequest["operation"];

/** Root-relative project files passed to MCL's per-request filesystem. Uint8Array values are copied before transfer so callers can reuse them. */
export type ProjectFile = string | Uint8Array;
export type ProjectFiles = Readonly<Record<string, ProjectFile>>;

export type MclError = ProtocolError;

export type LintDiagnostic = ProtocolLintDiagnostic;

export type MclResult =
  | ({ operation: "parse" } & ParseResult)
  | { operation: "lint"; diagnostics: ProtocolLintDiagnostic[] }
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
  parse(source: string, options?: RequestOptions): Promise<MclResult & { operation: "parse" }>;
  lint(source: string, options?: RequestOptions): Promise<MclResult & { operation: "lint" }>;
  format(source: string, options?: RequestOptions): Promise<MclResult & { operation: "format" }>;
  check(source: string, files?: ProjectFiles, options?: RequestOptions): Promise<MclResult & { operation: "check" }>;
  evaluate(source: string, files?: ProjectFiles, options?: RequestOptions): Promise<MclResult & { operation: "evaluate" }>;
  render(source: string, files?: ProjectFiles, options?: RequestOptions): Promise<MclResult & { operation: "render" }>;
  dispose(): void;
}

export interface MclProject {
  parse(options?: ProjectOperationOptions): Promise<MclResult & { operation: "parse" }>;
  lint(options?: ProjectOperationOptions): Promise<MclResult & { operation: "lint" }>;
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

export declare class MclRuntimeError extends Error {
  readonly code: string;
  readonly line?: number;
  readonly column?: number;
}

export declare function createMclRuntime(options?: RuntimeOptions): MclRuntime;
