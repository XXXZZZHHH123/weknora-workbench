#!/usr/bin/env bash
# Fast local debugging: keep the existing runtime and external docreader.
# Production releases still use the complete Dockerfile.app (including anydoc).
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
docker image inspect weknora-eval-app:mcp-direct >/dev/null
build_dir="$root/.build/local-app"
mkdir -p "$build_dir"
compiler=weknora-local-go:1.26
if ! docker image inspect "$compiler" >/dev/null 2>&1; then
  cat > "$build_dir/Dockerfile.compiler" <<'DOCKER'
FROM golang:1.26-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends libsqlite3-dev && rm -rf /var/lib/apt/lists/*
DOCKER
  docker build -f "$build_dir/Dockerfile.compiler" -t "$compiler" "$build_dir"
fi
mod_cache="${LOCAL_GO_MOD_CACHE:-$HOME/go/pkg/mod}"
mkdir -p "$mod_cache"
docker run --rm --name weknora-local-compile \
  -v "$root/WeKnora:/app:ro" -v "$build_dir:/out" \
  -v "$mod_cache:/go/pkg/mod" -v weknora-local-go-build:/root/.cache/go-build \
  -w /app -e CGO_ENABLED=1 -e CGO_CFLAGS=-Wno-deprecated-declarations \
  "$compiler" go build -p 2 \
  -ldflags '-w -s -X github.com/Tencent/WeKnora/internal/handler.Version=local-development -X google.golang.org/protobuf/reflect/protoregistry.conflictPolicy=warn' \
  -o /out/WeKnora ./cmd/server
# Preserve a stable runtime base so successive debug builds do not stack layers.
if ! docker image inspect weknora-eval-app:local-runtime-base >/dev/null 2>&1; then
  docker image tag weknora-eval-app:mcp-direct weknora-eval-app:local-runtime-base
fi
context="$build_dir/context"
mkdir -p "$context"
cp "$build_dir/WeKnora" "$context/WeKnora"
for directory in config scripts migrations; do
  rm -rf "$context/$directory"
  cp -R "$root/WeKnora/$directory" "$context/$directory"
done
cat > "$context/Dockerfile" <<'DOCKER'
FROM weknora-eval-app:local-runtime-base
COPY WeKnora /app/WeKnora
COPY config /app/config
COPY scripts /app/scripts
COPY migrations /app/migrations
DOCKER
docker build -t weknora-eval-app:mcp-direct "$context"
echo 'Local app image ready. Run bash start.sh to restart services with it.'
