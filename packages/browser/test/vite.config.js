import { defineConfig } from "vite";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");

export default defineConfig({
  root: resolve(packageRoot, "test"),
  server: {
    fs: { allow: [packageRoot] },
  },
});
