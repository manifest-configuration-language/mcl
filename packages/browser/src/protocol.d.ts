/* Generated from cmd/mcl-wasm/protocol.schema.json. Do not edit. */

export type MCLWebAssemblyRequestAndResponseProtocol = Request | Response;
export type Request = {
  requestId?: number;
  revision?: number;
  operation:
    | "parse"
    | "lint"
    | "format"
    | "check"
    | "evaluate"
    | "render"
    | "completion"
    | "definition"
    | "hover"
    | "references"
    | "rename";
  position?: CompletionPosition;
  newName?: string;
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
export type ProjectPath1 = ProjectPath2;
export type ProjectPath2 = string;

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
    | HoverResult
    | ReferencesResult
    | RenameResult;
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
  sourceMap: SourceMap;
}
/**
 * Source map entries are limited by maxSourceMapBytes. Ranges use zero-based UTF-16 line and character positions with an exclusive end.
 */
export interface SourceMap {
  complete: boolean;
  entries: SourceMapEntry[];
}
export interface SourceMapEntry {
  document: number;
  /**
   * JSON Pointer into the rendered YAML document.
   */
  path: string;
  kind: "key" | "value";
  output: SourceRange;
  sources: SourceMapOrigin[];
}
export interface SourceRange {
  start: CompletionPosition;
  end: CompletionPosition;
}
export interface SourceMapOrigin {
  path: ProjectPath1;
  range: SourceRange;
  kind: "mcl" | "mcl-overlay-base" | "mcl-overlay-patch";
  multiplicity: "unique" | "repeated" | "ambiguous";
  occurrences: number;
}
export interface CompletionResult {
  isIncomplete: boolean;
  items: CompletionItem[];
}
export interface CompletionItem {
  label: string;
  kind: number;
  insertText?: string;
  detail?: string;
  documentation?: string;
}
export interface DefinitionResult {
  location: DefinitionLocation | null;
}
export interface DefinitionLocation {
  path: string;
  range: SourceRange;
}
export interface HoverResult {
  hover: HoverInfo | null;
}
export interface HoverInfo {
  contents: string;
  range: SourceRange;
}
export interface ReferencesResult {
  references: ReferenceLocation[];
}
export interface ReferenceLocation {
  path: ProjectPath1;
  range: SourceRange;
}
export interface RenameResult {
  edits: RenameEdit[];
}
export interface RenameEdit {
  path: ProjectPath1;
  range: SourceRange;
  newText: string;
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
