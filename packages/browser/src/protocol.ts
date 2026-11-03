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
    | "rename"
    | "renameFileImports";
  position?: CompletionPosition;
  newName?: string;
  oldPath?: ProjectPath;
  newPath?: ProjectPath;
  entrypoint?: ProjectPath2;
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
export type ProjectPath = ProjectPath1;
export type ProjectPath1 = string;
/**
 * Project-relative MCL source path. Defaults to main.mcl.
 */
export type ProjectPath2 = string;
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
    | HoverResult
    | ReferencesResult
    | RenameResult
    | RenameFileImportsResult;
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
  actions?: LintCodeAction[];
}
export interface LintCodeAction {
  kind: "removeUnreachableDefinition";
  /**
   * @minItems 1
   */
  edits: [RenameEdit, ...RenameEdit[]];
}
export interface RenameEdit {
  path: ProjectPath;
  range: SourceRange;
  newText: string;
}
export interface SourceRange {
  start: CompletionPosition;
  end: CompletionPosition;
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
export interface SourceMapOrigin {
  path: ProjectPath;
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
  standardLibrary?: StandardLibraryFunction;
}
export interface StandardLibraryFunction {
  module: string;
  name: string;
}
export interface ReferencesResult {
  references: ReferenceLocation[];
}
export interface ReferenceLocation {
  path: ProjectPath;
  range: SourceRange;
}
export interface RenameResult {
  edits: RenameEdit[];
}
export interface RenameFileImportsResult {
  oldPath: ProjectPath;
  newPath: ProjectPath;
  edits: RenameEdit[];
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
