/* Generated from cmd/mcl-wasm/protocol.schema.json. Do not edit. */

export type MCLWebAssemblyRequestAndResponseProtocol = Request | Response;
export type Request = {
  requestId?: number;
  revision?: number;
  operation: "parse" | "lint" | "format" | "check" | "evaluate" | "render" | "completion" | "definition" | "hover";
  position?: CompletionPosition;
  entrypoint?: ProjectPath;
  /**
   * UTF-8 entrypoint source, limited by the runtime.
   */
  source: string;
  /**
   * Project files are limited by the runtime; this count excludes the entrypoint.
   */
  files?: {
    [k: string]: FileInput;
  };
};
/**
 * Project-relative MCL source path. Defaults to main.mcl.
 */
export type ProjectPath = string;
/**
 * Binary files omit content and are supplied in the optional second run argument under the same path.
 */
export type FileInput = {
  encoding: "utf8" | "binary";
  content?: string;
};
export type LintResult = LintDiagnostic[];

export interface CompletionPosition {
  line: number;
  character: number;
}
export interface Response {
  requestId: number;
  revision?: number;
  ok: boolean;
  result?:
    | ParseResult
    | LintResult
    | FormatResult
    | CheckResult
    | EvaluateResult
    | RenderResult
    | CompletionResult
    | DefinitionResult
    | HoverResult;
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
export interface CompletionResult {
  isIncomplete: boolean;
  items: CompletionItem[];
}
export interface CompletionItem {
  label: string;
  kind: number;
  insertText?: string;
}
export interface DefinitionResult {
  location: DefinitionLocation | null;
}
export interface DefinitionLocation {
  path: string;
  range: SourceRange;
}
export interface SourceRange {
  start: CompletionPosition;
  end: CompletionPosition;
}
export interface HoverResult {
  hover: HoverInfo | null;
}
export interface HoverInfo {
  contents: string;
  range: SourceRange;
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
