import assert from "node:assert/strict";
import test from "node:test";
import { MessageChannel } from "node:worker_threads";
import { expose } from "comlink";
import { createMclRuntime } from "../dist/index.js";

class FakeWorker {
  channel = new MessageChannel();
  terminated = false;

  constructor(index, updates) {
    this.index = index;
    this.updates = updates;
    expose(
      {
        async initialize() {},
        updateProject(projectId, rawDelta, binaries) {
          updates.push({ index, projectId, delta: JSON.parse(rawDelta), binaries });
          return { requestId: 0, ok: true };
        },
        run(rawRequest, binaries, projectId) {
          const request = JSON.parse(rawRequest);
          return {
            requestId: request.requestId,
            revision: request.revision,
            ok: true,
            result: request.operation === "render" ? { yaml: "value: 1" } : {},
          };
        },
      },
      this.channel.port1,
    );
    this.channel.port1.start();
    this.channel.port2.start();
  }

  addEventListener(type, listener) {
    this.channel.port2.addEventListener(type, listener);
  }

  removeEventListener(type, listener) {
    this.channel.port2.removeEventListener(type, listener);
  }

  postMessage(message, transfer) {
    this.channel.port2.postMessage(message, transfer);
  }

  emit(message) {
    this.channel.port1.postMessage(message);
  }

  terminate() {
    this.terminated = true;
    this.channel.port1.close();
    this.channel.port2.close();
  }
}

test("project patches sync only changed files and preserve caller binary buffers", async () => {
  const updates = [];
  const workers = [];
  const runtime = createMclRuntime({
    workerFactory: () => {
      const worker = new FakeWorker(workers.length, updates);
      workers.push(worker);
      return worker;
    },
  });
  const binary = new Uint8Array([1, 2, 3]);

  try {
    const first = runtime.openProject({
      "main.mcl": "emit 1",
      "schema.bin": binary,
      "unused.txt": "remove me",
    });
    await first.run("render");
    assert.deepEqual([...binary], [1, 2, 3]);
    assert.deepEqual(Object.keys(updates[0].delta.files).sort(), [
      "main.mcl",
      "schema.bin",
      "unused.txt",
    ]);

    const second = first.patch({
      set: { "main.mcl": "emit 2" },
      delete: ["unused.txt"],
    });
    await second.run("render");
    assert.deepEqual(Object.keys(updates[1].delta.files), ["main.mcl"]);
    assert.deepEqual(updates[1].delta.deleted, ["unused.txt"]);
    assert.equal(updates[1].delta.reset, false);
    assert.deepEqual([...binary], [1, 2, 3]);
  } finally {
    runtime.dispose();
  }
});

test("a failed worker is recreated and receives the current project snapshot", async () => {
  const updates = [];
  const workers = [];
  const runtime = createMclRuntime({
    workerFactory: () => {
      const worker = new FakeWorker(workers.length, updates);
      workers.push(worker);
      return worker;
    },
  });

  try {
    const project = runtime.openProject({ "main.mcl": "emit 1", "data.json": "{}" });
    await project.run("render");
    workers[0].emit({ type: "fatal", message: "unexpected WASM exit" });
    await new Promise((resolve) => setImmediate(resolve));

    await project.run("render");
    assert.equal(workers.length, 2);
    assert.equal(workers[0].terminated, true);
    assert.equal(updates[1].delta.reset, true);
    assert.deepEqual(Object.keys(updates[1].delta.files).sort(), ["data.json", "main.mcl"]);
  } finally {
    runtime.dispose();
  }
});
