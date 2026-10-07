import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vitest/config';

const root = fileURLToPath(new URL('.', import.meta.url));

export default defineConfig({
  test: {
    // Worker threads instead of the default child processes. Almost none of this
    // suite's time is the tests themselves -- 19.5s of wall time against 6.1s of
    // test time -- and the rest is per-file worker startup and module loading.
    // Threads pay that once per worker instead of once per fork: 19.5s -> 12.7s,
    // with the same 722 tests passing.
    //
    // `isolate: false` would reach ~8.9s and is NOT safe here. Several files mock
    // the same module with different fixtures (clients-tab.test.tsx and
    // clients-tab-update-stall.test.tsx both mock '@tanstack/react-query'), and a
    // shared module registry lets whichever registered last win. It passes in
    // file order and fails under --sequence.shuffle. Fix the mock collisions
    // before reaching for it.
    pool: 'threads',
  },
  // tsconfig의 "@/*": ["./*"] 경로 별칭을 테스트에서도 동일하게 해석.
  resolve: {
    alias: [{ find: /^@\/(.*)$/, replacement: `${root}$1` }],
  },
});
