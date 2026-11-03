import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { execFileSync } from "node:child_process";

const pkg = JSON.parse(
  await import("node:fs").then((fs) =>
    fs.readFileSync(join(process.cwd(), "package.json"), "utf8")
  )
);

if (process.env.MCL_VERSION && pkg.version !== process.env.MCL_VERSION) {
  console.error(
    `Package version ${pkg.version} does not match expected MCL version ${process.env.MCL_VERSION}.`
  );
  process.exit(1);
}

await writeFile(
  join(process.cwd(), "browser-consumer.ts"),
  `
import { createMclRuntime } from '@manifest-configuration-language/browser';

const runtime = createMclRuntime();
const project = runtime.openProject({ 'main.mcl': 'emit 1' });
const rendered = await project.run('render');
rendered.yaml satisfies string;
const completion = await project.run('completion', { position: { line: 0, character: 0 } });
completion.items satisfies unknown[];
// @ts-expect-error Completion requires a source position.
project.run('completion');
runtime.dispose();
`,
);

execFileSync(
  process.execPath,
  [
    join(process.cwd(), "node_modules/typescript/bin/tsc"),
    "--noEmit",
    "--strict",
    "--skipLibCheck",
    "--target",
    "ES2022",
    "--module",
    "NodeNext",
    "--moduleResolution",
    "NodeNext",
    "browser-consumer.ts",
  ],
  { stdio: "inherit" },
);

console.log(`Verified installed MCL packages ${pkg.version}.`);
