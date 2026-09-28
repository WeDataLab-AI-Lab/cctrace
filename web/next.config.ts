import type { NextConfig } from 'next';

const nextConfig: NextConfig = {
  output: 'export',
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
