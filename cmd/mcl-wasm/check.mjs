import { execFileSync, spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import { performance } from 'node:perf_hooks';
import { brotliCompressSync, constants as zlibConstants, gzipSync } from 'node:zlib';
import { maxSourceBytes } from '../../packages/browser/src/limits.js';

const maxStartupMs = 3000;
const maxRenderMs = 3000;
const workspace = process.cwd();
const sizeBudget = JSON.parse(readFileSync(path.join(workspace, 'packages/browser/performance-budget.json'), 'utf8'));
const buildDirectory = mkdtempSync(path.join(tmpdir(), 'mcl-wasm-check-'));
const wasmPath = path.join(buildDirectory, 'mcl.wasm');
let exitCode = 0;
const fail = (message) => { throw new Error(message); };

try {
  const env = { ...process.env, GOOS: 'js', GOARCH: 'wasm', CGO_ENABLED: '0' };
  const build = spawnSync('go', ['build', '-trimpath', '-ldflags=-s -w', '-o', wasmPath, './cmd/mcl-wasm'], {
    cwd: workspace,
    env,
    encoding: 'utf8',
  });
  if (build.status !== 0) fail(build.stderr || build.stdout || 'Go WebAssembly build failed');

  const wasm = readFileSync(wasmPath);
  const gzipBytes = gzipSync(wasm, { level: 9 }).byteLength;
  const brotliBytes = brotliCompressSync(wasm, { params: { [zlibConstants.BROTLI_PARAM_QUALITY]: 5 } }).byteLength;
  console.log(`mcl.wasm raw=${wasm.byteLength} bytes gzip=${gzipBytes} bytes brotli=${brotliBytes} bytes`);
  for (const [format, size] of Object.entries({ raw: wasm.byteLength, gzip: gzipBytes })) {
    const baseline = sizeBudget.baselineBytes[format];
    const maximum = Math.ceil(baseline * (1 + sizeBudget.maxGrowthPercent / 100));
    if (size > maximum) fail(`WASM ${format} size ${size} bytes exceeds the ${maximum}-byte budget (${sizeBudget.maxGrowthPercent}% growth from ${baseline})`);
  }

  const goroot = execFileSync('go', ['env', 'GOROOT'], { encoding: 'utf8' }).trim();
  const require = createRequire(import.meta.url);
  const nodeFs = require('node:fs');
  const projectPath = path.join(buildDirectory, 'project');
  const fsMethods = ['chmod', 'chown', 'close', 'fchmod', 'fchown', 'fstat', 'fsync', 'ftruncate', 'lchown', 'link', 'lstat', 'mkdir', 'open', 'read', 'readdir', 'readlink', 'rename', 'rmdir', 'stat', 'symlink', 'truncate', 'unlink', 'utimes', 'write'];
  globalThis.fs = { constants: nodeFs.constants };
  for (const method of fsMethods) {
    globalThis.fs[method] = (...args) => {
      const callback = args.pop();
      try {
        const mappedArgs = args.map((value) => typeof value === 'string' && (value === '/mcl-project' || value.startsWith('/mcl-project/'))
          ? path.join(projectPath, value.slice('/mcl-project'.length))
          : value);
        const value = nodeFs[`${method}Sync`](...mappedArgs);
        callback(null, value);
      } catch (error) {
        callback(error);
      }
    };
  }
  globalThis.path = require('node:path');
  globalThis.process = process;
  require(path.join(goroot, 'lib', 'wasm', 'wasm_exec.js'));
  const go = new globalThis.Go();
  const started = performance.now();
  const { instance } = await WebAssembly.instantiate(wasm, go.importObject);
  go.run(instance);
  while (!globalThis.mclWasmReady) await new Promise((resolve) => setTimeout(resolve, 0));
  const startupMs = performance.now() - started;
  console.log(`mcl.wasm startup=${startupMs.toFixed(1)} ms`);
  if (startupMs > maxStartupMs) fail(`WASM startup exceeds ${maxStartupMs} ms`);

  const nodesFixture = readFileSync(path.join(workspace, 'cmd/mcl-wasm/testdata/examples/references/clone-node/main.mcl'), 'utf8');
  const rulesFixture = readFileSync(path.join(workspace, 'cmd/mcl-wasm/testdata/examples/validation/rules/main.mcl'), 'utf8');
  const nodeDeclarations = nodesFixture.replace(/^emit .*\n/gm, '').trimEnd();
  const nodeEmits = [...nodesFixture.matchAll(/^emit .*$/gm)].map(([line]) => line).join('\n');
  const ruleDeclarations = rulesFixture.replace(/^emit container\n?/m, '').replace(/^validate container using hardened\n?/m, '').trimEnd();
  const source = `${nodeDeclarations}\n${ruleDeclarations}\n${nodeEmits}\nemit container\nvalidate container using hardened\n`;
  const invoke = (operation, inputSource = source, files = {}) => {
    rmSync(projectPath, { recursive: true, force: true });
    mkdirSync(projectPath, { recursive: true });
    writeFileSync(path.join(projectPath, 'main.mcl'), inputSource);
    for (const [relativePath, content] of Object.entries(files)) {
      const filePath = path.join(projectPath, relativePath);
      mkdirSync(path.dirname(filePath), { recursive: true });
      writeFileSync(filePath, content);
    }

    return JSON.parse(globalThis.mclWasm.run(JSON.stringify({
      requestId: 1, revision: 9, operation, source: inputSource,
      files: Object.fromEntries(Object.entries(files).map(([relativePath, content]) => [relativePath, { encoding: 'utf8', content }])),
    })));
  };
  const runNative = (command, inputSource, files = {}) => {
    const nativeProject = path.join(buildDirectory, `cli-${command}`);
    mkdirSync(nativeProject, { recursive: true });
    const inputPath = path.join(nativeProject, 'main.mcl');
    writeFileSync(inputPath, inputSource);
    for (const [relativePath, content] of Object.entries(files)) {
      const filePath = path.join(nativeProject, relativePath);
      mkdirSync(path.dirname(filePath), { recursive: true });
      writeFileSync(filePath, content);
    }

    return spawnSync('go', ['run', './cmd/mcl', command, inputPath], {
      cwd: workspace,
      encoding: 'utf8',
    });
  };
  for (const operation of ['parse', 'lint', 'format', 'evaluate', 'check']) {
    const response = invoke(operation);
    if (!response.ok || response.requestId !== 1 || response.revision !== 9) {
      fail(`${operation} protocol check failed: ${JSON.stringify(response)}`);
    }
  }
  const parsed = invoke('parse', 'let value = "ok"\nemit value\n');
  if (parsed.result?.declarations !== 2) fail(`parse API result check failed: ${JSON.stringify(parsed)}`);
  const lintSource = 'let unused = "😀"\nemit "ok"\n';
  const lintResult = invoke('lint', lintSource);
  const lintNative = runNative('lint', lintSource);
  const lintDiagnostic = lintResult.result?.[0];
  const nativeLintDiagnostic = lintNative.stderr.match(/:(\d+):(\d+): warning \[([^\]]+)\]: (.+)$/m);
  if (lintNative.status !== 0 || lintResult.result?.length !== 1 || !nativeLintDiagnostic) {
    fail(`lint diagnostics missing: CLI=${lintNative.stderr} WASM=${JSON.stringify(lintResult)}`);
  }
  const [, lintLine, lintColumn, lintCode, lintMessage] = nativeLintDiagnostic;
  if (lintDiagnostic.path !== 'main.mcl' || lintDiagnostic.line !== Number(lintLine) || lintDiagnostic.column !== Number(lintColumn)
    || lintDiagnostic.code !== lintCode || lintDiagnostic.message !== lintMessage || lintDiagnostic.severity !== 'warning') {
    fail(`lint diagnostics CLI/WASM mismatch: CLI=${JSON.stringify({ line: Number(lintLine), column: Number(lintColumn), code: lintCode, message: lintMessage })} WASM=${JSON.stringify(lintDiagnostic)}`);
  }
  const formatSource = 'let value =\n    nested: "ok"\nemit value\n';
  const formatResult = invoke('format', formatSource);
  const formatNative = runNative('fmt', formatSource);
  if (formatNative.status !== 0 || formatResult.result?.source !== formatNative.stdout) {
    fail(`format CLI/WASM output differs: CLI=${formatNative.stdout} WASM=${JSON.stringify(formatResult)}`);
  }
  const renderStarted = performance.now();
  const rendered = invoke('render');
  const renderMs = performance.now() - renderStarted;
  if (!rendered.ok || !rendered.result.yaml.includes('name: "api"')) {
    fail(`render smoke failed: ${JSON.stringify(rendered)}`);
  }
  const evaluated = invoke('evaluate');
  const checked = invoke('check');
  if (!evaluated.ok || evaluated.result?.artifacts !== rendered.result.artifacts || evaluated.result?.renderedBytes !== Buffer.byteLength(rendered.result.yaml)
    || !checked.ok || checked.result?.valid !== true || checked.result?.artifacts !== rendered.result.artifacts) {
    fail(`Evaluate/Check API result mismatch: render=${JSON.stringify(rendered.result)} evaluate=${JSON.stringify(evaluated)} check=${JSON.stringify(checked)}`);
  }
  const sourcePath = path.join(buildDirectory, 'wasm-smoke.mcl');
  writeFileSync(sourcePath, source);
  const nativeRender = spawnSync('go', ['run', './cmd/mcl', 'render', sourcePath], {
    cwd: workspace,
    encoding: 'utf8',
  });
  if (nativeRender.status !== 0) fail(nativeRender.stderr || 'native CLI parity command failed');
  if (nativeRender.stdout !== rendered.result.yaml) fail('CLI and WebAssembly render output differ');
  console.log(`mcl.wasm render=${renderMs.toFixed(1)} ms`);
  if (renderMs > maxRenderMs) fail(`WASM render exceeds ${maxRenderMs} ms`);

  const invalidSource = source.replace('allowPrivilegeEscalation: false', 'allowPrivilegeEscalation: true');
  writeFileSync(sourcePath, invalidSource);
  const nativeFailure = spawnSync('go', ['run', './cmd/mcl', 'render', sourcePath], {
    cwd: workspace,
    encoding: 'utf8',
  });
  if (nativeFailure.status === 0) fail('native CLI accepted the invalid Rule fixture');
  const wasmFailure = invoke('render', invalidSource);
  if (!wasmFailure.error || !nativeFailure.stderr.includes(wasmFailure.error.message)) {
    fail(`Rule validation message parity failed: CLI=${nativeFailure.stderr} WASM=${JSON.stringify(wasmFailure)}`);
  }

  const manifestSource = readFileSync(path.join(workspace, 'cmd/mcl-wasm/testdata/examples/adapters/manifest/main.mcl'), 'utf8');
  const manifestFiles = {
    'fixtures/resources.yaml': readFileSync(path.join(workspace, 'cmd/mcl-wasm/testdata/examples/adapters/manifest/fixtures/resources.yaml'), 'utf8'),
  };
  const manifestResult = invoke('render', manifestSource, manifestFiles);
  if (!manifestResult.ok || !manifestResult.result.yaml.includes('name:')) fail(`local manifest adapter check failed: ${JSON.stringify(manifestResult)}`);
  const runAdapterParity = (name, inputSource, files, wasmResult) => {
    if (!wasmResult.ok) fail(`local ${name} adapter check failed: ${JSON.stringify(wasmResult)}`);
    const adapterProject = path.join(buildDirectory, `${name.toLowerCase()}-adapter-project`);
    mkdirSync(adapterProject, { recursive: true });
    writeFileSync(path.join(adapterProject, 'main.mcl'), inputSource);
    for (const [relativePath, content] of Object.entries(files)) {
      const filePath = path.join(adapterProject, relativePath);
      mkdirSync(path.dirname(filePath), { recursive: true });
      writeFileSync(filePath, content);
    }
    const nativeResult = spawnSync('go', ['run', './cmd/mcl', 'render', path.join(adapterProject, 'main.mcl')], {
      cwd: workspace,
      encoding: 'utf8',
    });
    if (nativeResult.status !== 0) fail(`${name} adapter CLI render failed: ${nativeResult.stderr || nativeResult.stdout}`);
    if (nativeResult.stdout !== wasmResult.result.yaml) {
      fail(`${name} adapter CLI/WASM render output differs: CLI=${nativeResult.stdout} WASM=${wasmResult.result.yaml}`);
    }
    console.log(`${name} adapter CLI/WASM output matches`);
  };
  runAdapterParity('Manifest', manifestSource, manifestFiles, manifestResult);

  const importedSource = 'import "./modules.mcl" as modules\nemit modules.resource';
  const importResult = invoke('render', importedSource, { 'modules.mcl': 'export let resource =\n  apiVersion: "v1"\n  kind: "ConfigMap"\n  metadata:\n    name: "imported"\n' });
  if (!importResult.ok || !importResult.result.yaml.includes('name: "imported"')) fail(`virtual import check failed: ${JSON.stringify(importResult)}`);

  const unicodeInvalid = 'let broken = "😀" + )\nemit broken\n';
  const unicodeWasm = invoke('render', unicodeInvalid);
  const unicodeNative = runNative('render', unicodeInvalid);
  if (!unicodeWasm.error || unicodeWasm.error.code !== 'MCL-SYNTAX' || unicodeWasm.error.path !== 'main.mcl'
    || unicodeWasm.error.line !== 1 || unicodeWasm.error.column !== 1 || unicodeWasm.error.endLine !== 1
    || unicodeNative.status === 0 || !unicodeNative.stderr.includes(unicodeWasm.error.message)) {
    fail(`Unicode source diagnostic contract check failed: CLI=${unicodeNative.stderr} WASM=${JSON.stringify(unicodeWasm.error)}`);
  }

  const importedInvalid = invoke(
    'render',
    'import "./module.mcl" as module\nemit module.value\n',
    { 'module.mcl': 'export let value =\n\n\n  missing\n' },
  );
  if (importedInvalid.error?.path !== 'module.mcl' || importedInvalid.error.line !== 4) {
    fail(`Imported source diagnostic location check failed: ${JSON.stringify(importedInvalid.error)}`);
  }

  const tooLarge = invoke('parse', 'a'.repeat(maxSourceBytes + 1));
  if (tooLarge.ok || tooLarge.error?.code !== 'MCL-WASM-SOURCE-LIMIT') fail('source size limit check failed');
  const helmSource = readFileSync(path.join(workspace, 'cmd/mcl-wasm/testdata/examples/adapters/helm/main.mcl'), 'utf8');
  const helmFiles = {
    'chart/Chart.yaml': readFileSync(path.join(workspace, 'cmd/mcl-wasm/testdata/examples/adapters/helm/chart/Chart.yaml'), 'utf8'),
    'chart/values.yaml': readFileSync(path.join(workspace, 'cmd/mcl-wasm/testdata/examples/adapters/helm/chart/values.yaml'), 'utf8'),
    'chart/templates/deployment.yaml': readFileSync(path.join(workspace, 'cmd/mcl-wasm/testdata/examples/adapters/helm/chart/templates/deployment.yaml'), 'utf8'),
  };
  const helmResult = invoke('render', helmSource, helmFiles);
  runAdapterParity('Helm', helmSource, helmFiles, helmResult);

  const kustomizeSource = readFileSync(path.join(workspace, 'cmd/mcl-wasm/testdata/examples/adapters/kustomize/main.mcl'), 'utf8');
  const kustomizeFiles = Object.fromEntries(['fixtures/base/Kustomization', 'fixtures/base/deployment.yaml'].map((relativePath) => [
    relativePath, readFileSync(path.join(workspace, 'cmd/mcl-wasm/testdata/examples/adapters/kustomize', relativePath), 'utf8'),
  ]));
  const kustomizeResult = invoke('render', kustomizeSource, kustomizeFiles);
  runAdapterParity('Kustomize', kustomizeSource, kustomizeFiles, kustomizeResult);

  const additionalResourceSource = 'let resource =\n  apiVersion: "example.test/v1"\n  kind: "Widget"\n  metadata:\n    name: "sample"\nemit resource\n';
  const additionalResourceFiles = {
    'mcl.toml': '[kubernetes.resourceCatalog]\nrevision = "mcl-kubernetes-resource-catalog-v1"\n\n[[kubernetes.resourceCatalog.additionalResources]]\napiGroup = "example.test.walnuts.dev"\nkind = "Widget"\nresourceName = "widgets"\nscope = "Namespaced"\n',
  };
  const additionalResourceFailure = invoke('check', additionalResourceSource, additionalResourceFiles);
  const nativeProject = path.join(buildDirectory, 'additional-resource-project');
  mkdirSync(nativeProject, { recursive: true });
  writeFileSync(path.join(nativeProject, 'main.mcl'), additionalResourceSource);
  writeFileSync(path.join(nativeProject, 'mcl.toml'), additionalResourceFiles['mcl.toml']);
  const nativeAdditionalResourceFailure = spawnSync('go', ['run', './cmd/mcl', 'check', path.join(nativeProject, 'main.mcl')], { cwd: workspace, encoding: 'utf8' });
  if (nativeAdditionalResourceFailure.status === 0 || additionalResourceFailure.ok || !additionalResourceFailure.error || !nativeAdditionalResourceFailure.stderr.includes(additionalResourceFailure.error.message)) {
    fail(`AdditionalResources missing-schema CLI/WASM parity failed: CLI=${nativeAdditionalResourceFailure.stderr} WASM=${JSON.stringify(additionalResourceFailure)}`);
  }
  const crdSource = readFileSync(path.join(workspace, 'cmd/mcl-wasm/testdata/examples/validation/crd-validation/main.mcl'), 'utf8');
  const crdSuccess = invoke('check', crdSource, additionalResourceFiles);
  if (!crdSuccess.ok) fail(`AdditionalResources in-bundle schema check failed: ${JSON.stringify(crdSuccess)}`);
  const invalidCrd = invoke('check', crdSource.replace('size: 3', 'size: "large"'), additionalResourceFiles);
  if (!invalidCrd.error?.related?.length || !invalidCrd.error.related[0].path) {
    fail(`Related schema diagnostic location check failed: ${JSON.stringify(invalidCrd.error)}`);
  }
  writeFileSync(path.join(nativeProject, 'main.mcl'), crdSource);
  const nativeCrdSuccess = spawnSync('go', ['run', './cmd/mcl', 'render', path.join(nativeProject, 'main.mcl')], { cwd: workspace, encoding: 'utf8' });
  const wasmCrdRender = invoke('render', crdSource, additionalResourceFiles);
  if (nativeCrdSuccess.status !== 0 || !wasmCrdRender.ok || nativeCrdSuccess.stdout !== wasmCrdRender.result.yaml) {
    fail(`AdditionalResources in-bundle schema CLI/WASM parity failed: CLI=${nativeCrdSuccess.stderr} WASM=${JSON.stringify(wasmCrdRender)}`);
  }
  const unknownResourceSource = 'let resource =\n  apiVersion: "unknown.example/v1"\n  kind: "Unknown"\n  metadata:\n    name: "sample"\nemit resource\n';
  const unknownResourceWasm = invoke('check', unknownResourceSource);
  writeFileSync(path.join(nativeProject, 'main.mcl'), unknownResourceSource);
  const nativeUnknownResource = spawnSync('go', ['run', './cmd/mcl', 'check', path.join(nativeProject, 'main.mcl')], { cwd: workspace, encoding: 'utf8' });
  if (nativeUnknownResource.status === 0 || unknownResourceWasm.ok || !unknownResourceWasm.error || !nativeUnknownResource.stderr.includes(unknownResourceWasm.error.message)) {
    fail(`Unknown GVK CLI/WASM parity failed: CLI=${nativeUnknownResource.stderr} WASM=${JSON.stringify(unknownResourceWasm)}`);
  }

  const largeManifestSource = 'let resources = manifest.load("large.yaml")\nemit resources\n';
  const largeManifest = `apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: large-check\ndata:\n  payload: "${'x'.repeat(1_100_000)}"\n`;
  const largeCheck = invoke('check', largeManifestSource, { 'large.yaml': largeManifest });
  const largeCheckProject = path.join(buildDirectory, 'large-check-project');
  mkdirSync(largeCheckProject, { recursive: true });
  writeFileSync(path.join(largeCheckProject, 'main.mcl'), largeManifestSource);
  writeFileSync(path.join(largeCheckProject, 'large.yaml'), largeManifest);
  const nativeLargeCheck = spawnSync('go', ['run', './cmd/mcl', 'check', path.join(largeCheckProject, 'main.mcl')], { cwd: workspace, encoding: 'utf8' });
  if (nativeLargeCheck.status !== 0 || !largeCheck.ok) {
    fail(`Large output check CLI/WASM parity failed: CLI=${nativeLargeCheck.stderr} WASM=${JSON.stringify(largeCheck)}`);
  }
} catch (error) {
  console.error(error);
  exitCode = 1;
} finally {
  rmSync(buildDirectory, { recursive: true, force: true });
}
process.exit(exitCode);
