import PQueue from "p-queue";
import {
  operationPriorities,
  latestOnlyOperations,
  requestPriorities,
} from "./project.js";

export type RuntimeQueueTiming = {
  executionStartedAt?: number;
  executionDurationMs?: number;
};

export function createRuntimeScheduler() {
  const queue = new PQueue({ concurrency: 1 });
  queue.on("error", () => {});
  const signals = new Set<AbortController>();
  const latestPending = new Map<
    string,
    Map<number, { controller: AbortController; started: boolean }>
  >();

  function abortError(): DOMException {
    return new DOMException("The operation was aborted", "AbortError");
  }

  function enqueue<T>(
    operation: string,
    run: () => Promise<T>,
    signal: AbortSignal | undefined,
    scope: string,
    requestedPriority: string | undefined,
    timing: RuntimeQueueTiming = {},
  ): Promise<T> {
    if (signal?.aborted) return Promise.reject(signal.reason ?? abortError());
    const priority =
      (requestPriorities as Record<string, number>)[requestedPriority ?? ""] ??
      operationPriorities[operation] ??
      0;

    const key = (name: string) => JSON.stringify([scope, name]);
    const pendingKey = key(operation);

    const pendingGroup = () => {
      let group = latestPending.get(pendingKey);
      if (!group) {
        group = new Map();
        latestPending.set(pendingKey, group);
      }
      return group;
    };

    const removePending = (
      pendingPriority: number,
      pending: { controller: AbortController; started: boolean },
    ) => {
      const group = latestPending.get(pendingKey);
      if (group?.get(pendingPriority) !== pending) return;
      group.delete(pendingPriority);
      if (group.size === 0) latestPending.delete(pendingKey);
    };

    const supersedePending = (name: string, maximumPriority: number, message: string) => {
      const groupKey = key(name);
      const group = latestPending.get(groupKey);
      if (!group) return;
      for (const [pendingPriority, pending] of group) {
        if (pendingPriority > maximumPriority) continue;
        group.delete(pendingPriority);
        pending.controller.abort(new DOMException(message, "AbortError"));
      }
      if (group.size === 0) latestPending.delete(groupKey);
    };

    if (["render", "evaluate", "check"].includes(operation)) {
      supersedePending("lint", priority, "An explicit run replaced this pending lint");
    } else if (priority > requestPriorities.background && operation !== "lint") {
      supersedePending(
        "lint",
        requestPriorities.background,
        "An interactive operation replaced this pending background lint",
      );
    }

    const queueController = new AbortController();
    const pending = { controller: queueController, started: false };
    if (latestOnlyOperations.has(operation)) {
      supersedePending(operation, priority, "A newer operation replaced this request");
      pendingGroup().set(priority, pending);
    }

    signals.add(queueController);
    const abortWhileQueued = () => {
      if (!pending.started) queueController.abort(signal?.reason ?? abortError());
    };
    signal?.addEventListener("abort", abortWhileQueued, { once: true });

    return queue
      .add(
        async () => {
          pending.started = true;
          if (latestOnlyOperations.has(operation)) removePending(priority, pending);
          timing.executionStartedAt = performance.now();
          try {
            return (await run()) as T;
          } finally {
            timing.executionDurationMs = Math.max(
              0,
              performance.now() - timing.executionStartedAt!,
            );
          }
        },
        { priority, signal: queueController.signal },
      )
      .finally(() => {
        signal?.removeEventListener("abort", abortWhileQueued);
        signals.delete(queueController);
        if (latestOnlyOperations.has(operation)) removePending(priority, pending);
      }) as Promise<T>;
  }

  function abortAll(reason: Error): void {
    for (const controller of signals) controller.abort(reason);
  }

  return { enqueue, abortAll };
}
