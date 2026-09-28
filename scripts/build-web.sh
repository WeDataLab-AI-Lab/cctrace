#!/bin/bash
set -e

echo "Building Next.js dashboard..."
cd web
# cctraced serves the embedded dashboard and REST API from one origin. An
# absolute URL from a developer's .env.local (for example localhost while the
# browser uses 127.0.0.1) makes SameSite auth cookies disappear after login.
# Standalone `npm run dev` still honors NEXT_PUBLIC_API_URL; embedded builds do not.
NEXT_PUBLIC_API_URL= npm run build
cd ..

echo "Copying to embed directory..."
rm -rf internal/web/dist
mkdir -p internal/web/dist
cp -r web/out/. internal/web/dist/

echo "Done. Run: go build ./..."
