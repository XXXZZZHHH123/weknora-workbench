#!/usr/bin/env bash
set -euo pipefail
workbench_root="$(cd "$(dirname "$0")/.." && pwd)"
frontend_root="$workbench_root/../WeKnora/frontend"
build_context="$(mktemp -d /private/tmp/weknora-ui-build.XXXXXX)"
cd "$frontend_root"
npm run type-check
VITE_IS_DOCKER=true npm run build
cp -R dist "$build_context/dist"
cp "$workbench_root/deploy/structured-proxy.conf" "$build_context/structured-proxy.conf"
cp "$workbench_root/../commodity-research/deploy/research-proxy.conf" "$build_context/research-proxy.conf"
cp "$workbench_root/deploy/Dockerfile.frontend" "$build_context/Dockerfile.frontend"
docker build --pull=false -t weknora-eval-ui:field-resolution -f "$build_context/Dockerfile.frontend" "$build_context"
printf 'Frontend image ready. Build context: %s\n' "$build_context"
