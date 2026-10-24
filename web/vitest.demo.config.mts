import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vitest/config';

const root = fileURLToPath(new URL('.', import.meta.url));

// Checks that need a recorded demo (public/demo-data, or DEMO_DATA): run with
// `npm run test:demo-data` after cmd/demo-seed has recorded one. They are not
// part of `npm test`, which has no recording to read.
export default defineConfig({
  test: {
    include: ['lib/demo/**/*.check.ts'],
    testTimeout: 300_000,
  },
  resolve: {
    alias: [{ find: /^@\/(.*)$/, replacement: `${root}$1` }],
  },
});
