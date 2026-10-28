#!/bin/sh
# Builds web/ (only what the browser receives) and the server binary that embeds it.
set -e
rm -rf web && mkdir web
npm ci --no-fund --no-audit
tinygo build -o web/cycles.wasm -target wasm -no-debug -opt=z -panic=trap ./wasm
npx esbuild "$(tinygo env TINYGOROOT)/targets/wasm_exec.js" --minify --legal-comments=none > web/app.js
npx esbuild frontend/app.js --bundle --minify --format=iife --legal-comments=none >> web/app.js
cp frontend/index.html frontend/style.css web/
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o stockcycles .
