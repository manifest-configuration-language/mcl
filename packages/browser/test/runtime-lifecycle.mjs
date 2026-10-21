import assert from "node:assert/strict";
import { createMclRuntime } from "../src/index.js";

class FakeWorker {
  listeners = new Map();
  terminated = false;

  constructor({ configure, run }) {
    this.configure = configure;
    this.run = run;
  }

  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) ?? new Set();
    listeners.add(listener);
    this.listeners.set(type, listeners);
  }

  removeEventListener(type, listener) {
    this.listeners.get(type)?.delete(listener);
  }

  emit(type, data) {
    for (const listener of this.listeners.get(type) ?? []) listener({ data, message: data?.message });
  }

  postMessage(message) {
    if (message.type === "configure") this.configure?.(this);
    if (message.type === "run") this.run?.(this, message);
  }

  terminate() {
    this.terminated = true;
  }
}

const ready = (worker) => worker.emit("message", { type: "ready" });
const respond = (worker, requestId) => worker.emit("message", {
  type: "response",
  requestId,
  value: { ok: true, result: {} },
});
const deferred = () => {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
};
const runtimeWithWorkers = (makeWorker, options = {}) => {
  const workers = [];
  const runtime = createMclRuntime({
    ...options,
    workerFactory: () => {
      const worker = makeWorker(workers.length);
      workers.push(worker);
      return worker;
    },
  });
  return { runtime, workers };
};

export async function testRuntimeLifecycle() {
  {
    let attempts = 0;
    const { runtime, workers } = runtimeWithWorkers(() => new FakeWorker({
      configure(worker) {
        if (attempts++ === 0) throw new Error("configure failed");
        ready(worker);
      },
      run(worker, request) { respond(worker, request.requestId); },
    }));
    await assert.rejects(runtime.parse("type A = string"), /configure failed/);
    assert.equal(workers[0].terminated, true);
    assert.equal((await runtime.parse("type A = string")).operation, "parse");
    assert.equal(workers.length, 2, "initialization failure should allow a fresh Worker retry");
    runtime.dispose();
  }

  {
    const { runtime, workers } = runtimeWithWorkers((index) => new FakeWorker({
      configure(worker) { if (index !== 0) ready(worker); },
      run(worker, request) { respond(worker, request.requestId); },
    }), { initializationTimeoutMs: 10 });
    await assert.rejects(runtime.parse("type A = string"), /initialization timed out/);
    assert.equal(workers[0].terminated, true);
    assert.equal((await runtime.parse("type A = string")).operation, "parse");
    assert.equal(workers.length, 2, "initialization timeout should allow a fresh Worker retry");
    runtime.dispose();
  }

  {
    const initStarted = deferred();
    const { runtime, workers } = runtimeWithWorkers((index) => new FakeWorker({
      configure(worker) { if (index === 0) initStarted.resolve(); else ready(worker); },
      run(worker, request) { respond(worker, request.requestId); },
    }));
    const controller = new AbortController();
    const pending = runtime.parse("type A = string", { signal: controller.signal });
    await initStarted.promise;
    controller.abort();
    await assert.rejects(pending, { name: "AbortError" });
    assert.equal(workers[0].terminated, true);
    await runtime.parse("type A = string");
    assert.equal(workers.length, 2, "aborting initialization should allow a fresh Worker retry");
    runtime.dispose();
  }

  {
    const { runtime, workers } = runtimeWithWorkers(() => new FakeWorker({
      configure: ready,
      run(worker, request) {
        assert.deepEqual([...request.files["schema.bin"]], [1, 2, 3]);
        respond(worker, request.requestId);
      },
    }));
    const input = Buffer.from([1, 2, 3]);
    const project = runtime.openProject({ "main.mcl": "emit 1", "schema.bin": input });
    input[0] = 9;
    await project.evaluate();
    assert.equal(input.byteLength, 3, "project setup should not detach or alias the caller's Buffer");
    runtime.dispose();
    assert.equal(workers[0].terminated, true);
  }

  {
    const firstRun = deferred();
    const { runtime, workers } = runtimeWithWorkers((index) => new FakeWorker({
      configure: ready,
      run(worker, request) {
        if (index === 0) firstRun.resolve();
        else {
          worker.emit("message", { type: "response", requestId: request.requestId - 1, value: { ok: true, result: {} } });
          respond(worker, request.requestId);
        }
      },
    }));
    await assert.rejects(runtime.parse("type A = string", { timeoutMs: 10 }), (error) => error.code === "MCL-WASM-TIMEOUT");
    await firstRun.promise;
    assert.equal(workers[0].terminated, true);
    assert.equal((await runtime.parse("type A = string")).operation, "parse");
    assert.equal(workers.length, 2, "operation timeout should recreate the Worker");
    runtime.dispose();
  }

  {
    const runPosted = deferred();
    const { runtime, workers } = runtimeWithWorkers((index) => new FakeWorker({
      configure: ready,
      run(worker, request) {
        if (index === 0) runPosted.resolve();
        else respond(worker, request.requestId);
      },
    }));
    const controller = new AbortController();
    const pending = runtime.parse("type A = string", { signal: controller.signal });
    await runPosted.promise;
    controller.abort();
    await assert.rejects(pending, { name: "AbortError" });
    assert.equal(workers[0].terminated, true);
    await runtime.parse("type A = string");
    assert.equal(workers.length, 2, "aborting an operation should allow a fresh Worker retry");
    runtime.dispose();
  }

  {
    const runPosted = deferred();
    const { runtime, workers } = runtimeWithWorkers(() => new FakeWorker({
      configure: ready,
      run() { runPosted.resolve(); },
    }));
    const pending = runtime.parse("type A = string");
    await runPosted.promise;
    runtime.dispose();
    await assert.rejects(pending, /disposed/);
    assert.equal(workers[0].terminated, true);
    await assert.rejects(runtime.parse("type A = string"), /disposed/);
  }
}
