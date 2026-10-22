import { defineConfig } from "vite";
import { realpathSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const packageRoot = realpathSync(resolve(dirname(fileURLToPath(import.meta.url)), ".."));
const repositoryRoot = resolve(packageRoot, "../..");
const fixtureRoot = realpathSync(resolve(repositoryRoot, "cmd/mcl-wasm/testdata"));
const browserPackageRoot = process.env.MCL_BROWSER_PACKAGE ? realpathSync(process.env.MCL_BROWSER_PACKAGE) : undefined;

export default defineConfig({
  root: resolve(packageRoot, "test"),
  resolve: {
    alias: {
      "@mcl/browser": resolve(browserPackageRoot ?? packageRoot, "src/index.js"),
    },
  },
  server: {
    fs: { allow: [packageRoot, fixtureRoot, ...(browserPackageRoot ? [browserPackageRoot] : [])] },
  },
});
