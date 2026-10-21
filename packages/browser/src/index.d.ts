export type MclOperation = "parse" | "lint" | "format" | "check" | "evaluate" | "render";

export type ProjectFile = string | Uint8Array;
export type ProjectFiles = Readonly<Record<string, ProjectFile>>;

export interface MclError {
  code: string;
  message: string;
  line?: number;
  column?: number;
}

export interface LintDiagnostic {
  code: string;
  message: string;
  severity: string;
  line: number;
  column: number;
}

export type MclResult =
  | { operation: "parse"; declarations: number }
  | { operation: "lint"; diagnostics: LintDiagnostic[] }
  | { operation: "format"; source: string }
  | { operation: "check"; valid: true; artifacts: number }
  | { operation: "evaluate"; artifacts: number; renderedBytes: number }
  | { operation: "render"; yaml: string; artifacts: number };

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
