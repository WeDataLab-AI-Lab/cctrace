import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vitest/config';

const root = fileURLToPath(new URL('.', import.meta.url));

// tsconfig의 "@/*": ["./*"] 경로 별칭을 테스트에서도 동일하게 해석.
export default defineConfig({
  resolve: {
    alias: [{ find: /^@\/(.*)$/, replacement: `${root}$1` }],
  },
});
