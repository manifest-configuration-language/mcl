// Public API for @manifest-configuration-language/browser

export {
  createMclRuntime,
  MclRuntimeError,
} from "./runtime.js";
export type {
  MclOperation,
  MclResult,
  MclRuntime,
  MclProject,
  RuntimeStatus,
  RuntimeStatusEvent,
  RuntimeOperationOutcome,
  RuntimeOperationEvent,
  RuntimeOptions,
} from "./runtime.js";

export {
  isValidProjectPath,
  isValidProjectFileSet,
} from "./project.js";
export type {
  ProjectFile,
  ProjectFiles,
  RequestOptions,
  ProjectOperationOptions,
  ProjectParametersOptions,
  ProjectCompletionOptions,
  ProjectNavigationOptions,
  ProjectRenameOptions,
  ProjectFileRenameOptions,
} from "./project.js";

export {
  maxSourceBytes,
  maxSourceMapBytes,
  maxRenameNameLength,
  maxProjectFiles,
  maxProjectFileBytes,
  maxProjectBytes,
  maxProjectPathLength,
  maxProjectPathDepth,
} from "./limits.js";

export type {
  CheckResult,
  CompletionItem,
  CompletionPosition,
  CompletionResult,
  DefinitionLocation,
  DefinitionResult,
  EvaluateResult,
  FormatResult,
  HoverInfo,
  HoverResult,
  LintCodeAction,
  LintDiagnostic,
  ParseResult,
  ReferenceLocation,
  ReferencesResult,
  RelatedLocation,
  RenderResult,
  RenameEdit,
  RenameFileImportsResult,
  RenameResult,
  SourceMap,
  SourceMapEntry,
  SourceMapOrigin,
  SourceRange,
} from "./protocol.js";
