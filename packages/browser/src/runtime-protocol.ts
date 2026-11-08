import type {
  Error as ProtocolError,
  LintDiagnostic,
  Request,
  Response,
} from "./protocol.js";

type RequestFields = {
  parameters?: Record<string, unknown>;
  position?: { line: number; character: number };
  newName?: string;
  oldPath?: string;
  newPath?: string;
};

export function createWasmRequest(
  requestId: number,
  operation: Request["operation"],
  entrypoint: string,
  source: string,
  options: RequestFields,
): Request {
  return {
    requestId,
    revision: requestId,
    operation,
    entrypoint,
    source,
    ...(options.parameters !== undefined ? { parameters: { ...options.parameters } } : {}),
    ...(options.position ? { position: options.position } : {}),
    ...(options.newName ? { newName: options.newName } : {}),
    ...(options.oldPath ? { oldPath: options.oldPath } : {}),
    ...(options.newPath ? { newPath: options.newPath } : {}),
  };
}

export function decodeWasmResponse(
  operation: Request["operation"],
  response: Response,
  makeError: (error: Partial<ProtocolError> & { code: string; message: string }) => Error,
): object {
  if (!response.ok) {
    throw makeError(response.error ?? { code: "MCL-WASM-RESPONSE", message: "Operation failed" });
  }
  if (operation === "lint") {
    return { operation, diagnostics: (response.result ?? []) as LintDiagnostic[] };
  }

  return { operation, ...(response.result as object) };
}
