/* Generated from cmd/mcl-wasm/protocol.schema.json. Do not edit. */

export type MCLWebAssemblyRequestAndResponseProtocol = Request | Response;
export type LintResult = LintDiagnostic[];

export interface Request {
  requestId?: unknown;
  revision?: number;
  operation: "parse" | "lint" | "format" | "check" | "evaluate" | "render";
  /**
   * Project-relative MCL source path. Defaults to main.mcl.
   */
  entrypoint?: string;
  /**
   * UTF-8 entrypoint source, limited to 65536 bytes by the runtime.
   */
  source: string;
  /**
   * Project files are limited to 16 MiB each and 64 MiB total including source.
   */
  files?: {
    [k: string]: FileInput;
  };
}
export interface FileInput {
  encoding: "utf8" | "base64";
  content: string;
}
export interface Response {
  requestId: unknown;
  revision?: number;
  ok: boolean;
  result?: ParseResult | LintResult | FormatResult | CheckResult | EvaluateResult | RenderResult;
  error?: Error;
}
export interface ParseResult {
  declarations: number;
}
export interface LintDiagnostic {
  code: string;
  message: string;
  severity: string;
  line: number;
  column: number;
}
export interface FormatResult {
  source: string;
}
export interface CheckResult {
  valid: true;
  artifacts: number;
}
export interface EvaluateResult {
  artifacts: number;
  renderedBytes: number;
}
export interface RenderResult {
  yaml: string;
  artifacts: number;
}
export interface Error {
  code: string;
  message: string;
  line?: number;
  column?: number;
}
