/* Generated from cmd/mcl-wasm/protocol.schema.json. Do not edit. */

export type MCLWebAssemblyRequestAndResponseProtocol = Request | Response;
/**
 * Binary files omit content and are supplied in the optional second run argument under the same path.
 */
export type FileInput = {
  encoding: "utf8" | "binary";
  content?: string;
};
export type LintResult = LintDiagnostic[];

export interface Request {
  requestId?: number;
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
export interface Response {
  requestId: number;
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
  path: string;
  line: number;
  column: number;
  endLine: number;
  endColumn: number;
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
  path?: string;
  line?: number;
  column?: number;
  endLine?: number;
  endColumn?: number;
  related?: RelatedLocation[];
}
export interface RelatedLocation {
  path: string;
  message: string;
  line: number;
  column: number;
  endLine: number;
  endColumn: number;
}
