import type { NextConfig } from 'next';

// Empty everywhere but the static demo, which is served under the docs site
// (/cctrace/demo). Client code reads the same variable for the few navigations
// that bypass the router (lib/base-path.ts).
const basePath = process.env.NEXT_PUBLIC_BASE_PATH || undefined;

const nextConfig: NextConfig = {
  output: 'export',
  basePath,
  // Always defined, so every build inlines a constant: an unset NEXT_PUBLIC_
  // variable stays a runtime lookup, and the demo-only code behind
  // `process.env.NEXT_PUBLIC_DEMO === '1'` would then ship in every build.
  env: {
    NEXT_PUBLIC_DEMO: process.env.NEXT_PUBLIC_DEMO ?? '',
    NEXT_PUBLIC_BASE_PATH: process.env.NEXT_PUBLIC_BASE_PATH ?? '',
  },
  reactCompiler: true,
  trailingSlash: true,
  images: {
    unoptimized: true,
  },
  turbopack: {
    root: __dirname,
  },
};

export default nextConfig;
