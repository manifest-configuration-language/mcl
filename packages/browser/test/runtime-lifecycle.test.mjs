import assert from "node:assert/strict";
import test from "node:test";
import { MessageChannel } from "node:worker_threads";
import { expose } from "comlink";
import { createMclRuntime } from "../dist/index.js";

class FakeWorker {
  listeners = new Map();
  channel = new MessageChannel();
  terminated = false;

  constructor(behavior = {}) {
    this.behavior = behavior;
    expose(
      {
        initialize: (...args) => behavior.initialize?.(this, ...args),
        updateProject: (...args) => behavior.updateProject?.(this, ...args) ?? { ok: true },
        run: (...args) => behavior.run?.(this, ...args) ?? { ok: true, result: {} },
      },
      this.channel.port1,
    );
    this.channel.port1.start();
    this.channel.port2.start();
  }

  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) ?? new Set();
    listeners.add(listener);
    this.listeners.set(type, listeners);
    this.channel.port2.addEventListener(type, listener);
  }

  removeEventListener(type, listener) {
    this.listeners.get(type)?.delete(listener);
    this.channel.port2.removeEventListener(type, listener);
  }

  postMessage(message, transfer) {
    this.channel.port2.postMessage(message, transfer);
  }

  emit(type, data = {}) {
    for (const listener of this.listeners.get(type) ?? [])
      listener({ data, message: data.message });
  }

  terminate() {
    this.terminated = true;
    this.channel.port1.close();
    this.channel.port2.close();
  }
}

function makeRuntime(createWorker, options = {}) {
  const workers = [];
  const runtime = createMclRuntime({
    ...options,
    workerFactory: () => {
      const worker = createWorker(workers.length);
      workers.push(worker);
      return worker;
    },
  });
  return { runtime, workers };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

const ready = () => Promise.resolve();
const runSuccess = (_worker, requestJSON) => {
  const request = JSON.parse(requestJSON);
  return { requestId: request.requestId, ok: true, result: {} };
};

test("initialization failures, timeouts, and aborts allow a fresh worker retry", async () => {
  for (const mode of ["failure", "timeout", "abort"]) {
    const started = deferred();
    const { runtime, workers } = makeRuntime(
      (index) =>
        new FakeWorker({
          initialize() {
            if (index > 0) return ready();
            if (mode === "failure") return Promise.reject(new Error("initialization failed"));
            started.resolve();
            return new Promise(() => {});
          },
          run: runSuccess,
        }),
      { initializationTimeoutMs: mode === "timeout" ? 10 : 1000 },
    );
    const project = runtime.openProject({ "main.mcl": "emit 1" });
    if (mode === "abort") {
      const controller = new AbortController();
      const pending = project.run("parse", { signal: controller.signal });
      await started.promise;
      controller.abort();
      await assert.rejects(pending, { name: "AbortError" });
    } else if (mode === "timeout") {
      await assert.rejects(project.run("parse"), /initialization timed out/);
    } else {
      await assert.rejects(project.run("parse"), /initialization failed/);
    }
    assert.equal(workers[0].terminated, true);
    await project.run("parse");
    assert.equal(workers.length, 2);
    runtime.dispose();
  }
});

test("operation timeout and abort terminate the worker and allow recovery", async () => {
  for (const mode of ["timeout", "abort"]) {
    const started = deferred();
    const { runtime, workers } = makeRuntime(
      (index) =>
        new FakeWorker({
          initialize: ready,
          run(_worker, requestJSON) {
            if (index === 0) {
              started.resolve();
              return new Promise(() => {});
            }
            return runSuccess(_worker, requestJSON);
          },
        }),
    );
    const project = runtime.openProject({ "main.mcl": "emit 1" });
    if (mode === "abort") {
      const controller = new AbortController();
      const pending = project.run("parse", { signal: controller.signal });
      await started.promise;
      controller.abort();
      await assert.rejects(pending, { name: "AbortError" });
    } else {
      await assert.rejects(
        project.run("parse", { timeoutMs: 10 }),
        (error) => error.code === "MCL-WASM-TIMEOUT",
      );
      await started.promise;
    }
    assert.equal(workers[0].terminated, true);
    await project.run("parse");
    assert.equal(workers.length, 2);
    runtime.dispose();
  }
});

test("queued aborts do not post a request or stop the active operation", async () => {
  const activeStarted = deferred();
  const activeRun = deferred();
  let posted = 0;
  const { runtime } = makeRuntime(
    () =>
      new FakeWorker({
        initialize: ready,
        run(_worker, requestJSON) {
          posted++;
          if (posted === 1) {
            activeStarted.resolve();
            return activeRun.promise.then(() => runSuccess(_worker, requestJSON));
          }
          return runSuccess(_worker, requestJSON);
        },
      }),
  );
  const project = runtime.openProject({ "main.mcl": "emit 1" });
  const active = project.run("parse");
  await activeStarted.promise;
  const controller = new AbortController();
  const queued = project.run("render", { signal: controller.signal });
  controller.abort();
  await assert.rejects(queued, { name: "AbortError" });
  assert.equal(posted, 1);
  activeRun.resolve();
  await active;
  runtime.dispose();
});

test("interactive requests pass queued background work", async () => {
  const started = deferred();
  const finishActive = deferred();
  const operations = [];
  const { runtime } = makeRuntime(
    () =>
      new FakeWorker({
        initialize: ready,
        run(_worker, requestJSON) {
          const request = JSON.parse(requestJSON);
          operations.push(request.operation);
          if (request.operation === "parse") {
            started.resolve();
            return finishActive.promise.then(() => ({
              requestId: request.requestId,
              ok: true,
              result: {},
            }));
          }
          return { requestId: request.requestId, ok: true, result: {} };
        },
      }),
  );
  const project = runtime.openProject({ "main.mcl": "emit 1" });
  const active = project.run("parse");
  await started.promise;
  const background = project.run("check", { priority: "background" });
  const interactive = project.run("render", { priority: "interactive" });
  finishActive.resolve();
  await Promise.all([active, background, interactive]);
  assert.deepEqual(operations, ["parse", "render", "check"]);
  runtime.dispose();
});

test("dispose terminates the active request and rejects later work", async () => {
  const started = deferred();
  const { runtime, workers } = makeRuntime(
    () =>
      new FakeWorker({
        initialize: ready,
        run() {
          started.resolve();
          return new Promise(() => {});
        },
      }),
  );
  const project = runtime.openProject({ "main.mcl": "emit 1" });
  const pending = project.run("parse");
  await started.promise;
  const queued = project.run("render");
  runtime.dispose();
  await assert.rejects(pending, /disposed/);
  await assert.rejects(queued, /disposed/);
  assert.equal(workers[0].terminated, true);
  await assert.rejects(project.run("parse"), /disposed/);
});

test("RPC rejection restarts the worker and restores the complete project", async () => {
  const updates = [];
  const { runtime, workers } = makeRuntime(
    (index) =>
      new FakeWorker({
        initialize: ready,
        updateProject(_worker, projectId, rawDelta, binaries) {
          updates.push({ index, projectId, delta: JSON.parse(rawDelta), binaries });
          return { ok: true };
        },
        run(_worker, requestJSON) {
          if (index === 0) throw new Error("RPC disconnected");
          return runSuccess(_worker, requestJSON);
        },
      }),
  );
  const project = runtime.openProject({
    "main.mcl": "emit 1",
    "schema.bin": Uint8Array.from([1, 2]),
  });
  await assert.rejects(project.run("parse"), /RPC disconnected/);
  await project.run("parse");
  assert.equal(workers.length, 2);
  assert.equal(workers[0].terminated, true);
  assert.equal(updates[0].delta.reset, true);
  assert.equal(updates[1].delta.reset, true);
  assert.deepEqual([...updates[0].binaries["schema.bin"]], [1, 2]);
  runtime.dispose();
});

test("project patches send only changed files, preserve buffers, and skip identical snapshots", async () => {
  const updates = [];
  const { runtime } = makeRuntime(
    () =>
      new FakeWorker({
        initialize: ready,
        updateProject(_worker, projectId, rawDelta, binaries) {
          updates.push({ projectId, delta: JSON.parse(rawDelta), binaries });
          return { ok: true };
        },
        run: runSuccess,
      }),
  );
  const binary = Uint8Array.from([1, 2, 3]);
  const first = runtime.openProject({
    "main.mcl": "emit 1",
    "schema.bin": binary,
    "remove.txt": "x",
  });
  await first.run("render");
  await first.run("lint");
  assert.equal(updates.length, 1);
  const second = first.patch({ set: { "main.mcl": "emit 2" }, delete: ["remove.txt"] });
  await second.run("render");
  assert.deepEqual(Object.keys(updates[1].delta.files), ["main.mcl"]);
  assert.deepEqual(updates[1].delta.deleted, ["remove.txt"]);
  assert.deepEqual([...binary], [1, 2, 3]);
  runtime.dispose();
});

test("stale worker events cannot terminate a newer worker", async () => {
  const { runtime, workers } = makeRuntime(
    (index) =>
      new FakeWorker({
        initialize: ready,
        run(_worker, requestJSON) {
          if (index === 0) throw new Error("replace worker");
          return runSuccess(_worker, requestJSON);
        },
      }),
  );
  const project = runtime.openProject({ "main.mcl": "emit 1" });
  await runtime.warmUp();
  const staleMessageHandlers = [...workers[0].listeners.get("message")];
  await assert.rejects(project.run("parse"), /replace worker/);
  await project.run("parse");
  for (const handler of staleMessageHandlers)
    handler({ data: { type: "fatal", message: "late failure" } });
  workers[0].emit("message", { type: "fatal", message: "late failure" });
  workers[0].emit("error", { message: "late error" });
  assert.equal(workers[1].terminated, false);
  await project.run("parse");
  runtime.dispose();
});

test("invalid project paths and entrypoints are rejected before worker startup", async () => {
  const { runtime, workers } = makeRuntime(
    () => new FakeWorker({ initialize: ready, run: runSuccess }),
  );
  assert.throws(() => runtime.openProject({}), /at least one MCL source file/);
  assert.throws(
    () => runtime.openProject({ "../main.mcl": "emit 1" }),
    /Project file paths must be valid/,
  );
  const project = runtime.openProject({
    "main.mcl": "emit 1",
    "schema.bin": Uint8Array.from([1, 2, 3]),
  });
  await assert.rejects(
    project.run("parse", { entrypoint: "missing.mcl" }),
    /entrypoint does not exist/,
  );
  assert.equal(
    workers.length,
    0,
    "invalid project requests should be rejected before creating a worker",
  );
  runtime.dispose();
});
