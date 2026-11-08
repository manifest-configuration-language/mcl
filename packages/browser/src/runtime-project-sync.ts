import * as Comlink from "comlink";
import type { WorkerApi } from "./worker.js";
import { computeProjectDelta } from "./project.js";
import type { ProjectSnapshot, RequestOptions } from "./project.js";
import type { Error as ProtocolError } from "./protocol.js";

type ProjectApi = Comlink.Remote<WorkerApi>;

export function createProjectSynchronizer(
  getApi: () => ProjectApi | undefined,
  runCall: <T>(signal: AbortSignal | undefined, call: () => Promise<T>) => Promise<T>,
  makeError: (error: Partial<ProtocolError> & { code: string; message: string }) => Error,
) {
  let workerProjectId: string | undefined;
  let workerSnapshot: ProjectSnapshot | undefined;

  async function sync(
    projectId: string,
    snapshot: ProjectSnapshot,
    requestOptions: RequestOptions,
  ): Promise<void> {
    if (workerProjectId === projectId && workerSnapshot === snapshot) return;
    const api = getApi()!;
    const previousSnapshot = workerProjectId === projectId ? workerSnapshot : undefined;
    const reset = workerProjectId !== projectId;
    const result = computeProjectDelta(snapshot, previousSnapshot, reset);
    if (result === null) {
      workerSnapshot = snapshot;
      return;
    }

    const { delta, binaryFiles, transferables } = result;
    const updateJSON = JSON.stringify(delta);
    const transferableBinaries: Record<string, Uint8Array> = Object.create(null);
    for (const [path, bytes] of Object.entries(binaryFiles)) {
      transferableBinaries[path] = bytes;
    }

    const response = await runCall(requestOptions.signal, () =>
      api.updateProject(
        projectId,
        updateJSON,
        Comlink.transfer(transferableBinaries, transferables),
      ),
    );
    if (!response.ok) {
      throw makeError(
        response.error ?? { code: "MCL-WASM-PROJECT", message: "Project update failed" },
      );
    }
    workerProjectId = projectId;
    workerSnapshot = snapshot;
  }

  function reset(): void {
    workerProjectId = undefined;
    workerSnapshot = undefined;
  }

  return { sync, reset };
}
