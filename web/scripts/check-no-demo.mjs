// Fails a production build that carries the static demo's replay layer
// (lib/demo/runtime.ts). That code answers API calls from recordings and is meant
// only for the docs-site demo, built with NEXT_PUBLIC_DEMO=1. next.config.ts
// keeps it out of every other build by always defining that flag; this is what
// notices if it stops doing so.
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

const MARKER = 'demo_read_only';
const root = 'out/_next';

if (process.env.NEXT_PUBLIC_DEMO === '1') process.exit(0);

const files = (dir) =>
  readdirSync(dir, { withFileTypes: true }).flatMap((entry) =>
    entry.isDirectory() ? files(join(dir, entry.name)) : [join(dir, entry.name)],
  );

const carrying = files(root).filter((file) => file.endsWith('.js') && readFileSync(file, 'utf8').includes(MARKER));
if (carrying.length > 0) {
  console.error(`The static demo's replay layer is in this production build (${carrying.join(', ')}).`);
  process.exit(1);
}
