import { defineConfig } from "vite";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { nodePolyfills } from "vite-plugin-node-polyfills";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");

export default defineConfig({
  root: resolve(packageRoot, "test"),
  plugins: [nodePolyfills({ protocolImports: true })],
  server: {
    fs: { allow: [packageRoot] },
  },
});
