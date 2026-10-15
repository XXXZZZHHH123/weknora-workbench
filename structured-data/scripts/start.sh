#!/bin/bash
set -euo pipefail
project_dir="$(cd "$(dirname "$0")/.." && pwd)"
workbench_dir="$(dirname "$project_dir")"
if ! docker info >/dev/null 2>&1; then
  echo "Docker 未运行，请先打开 Docker Desktop，再执行本脚本。"
  exit 1
fi
# Generate the local service secret once, before Compose binds the file.
mkdir -p "$workbench_dir/commodity-research/data"
if [[ ! -e "$workbench_dir/commodity-research/data/service-token" ]]; then
  (umask 077; openssl rand -hex 32 > "$workbench_dir/commodity-research/data/service-token")
fi
[[ -s "$workbench_dir/commodity-research/data/service-token" && -f "$workbench_dir/commodity-research/data/service-token" ]] || {
  echo "商品研究 service-token 必须是非空文件，请先检查绑定路径。" >&2
  exit 1
}
# Keep project names stable: they own the existing database volumes and network.
compose_args=(-f "$workbench_dir/WeKnora/docker-compose.yml" -f "$workbench_dir/compose.test.yml")
if [[ "${WORKBENCH_DOCUMENTS_ENABLED:-false}" == true || -f "$workbench_dir/.private/document-runtime.enabled" ]]; then
  compose_args+=(-f "$workbench_dir/compose.documents.yml")
fi
docker compose -p weknora-eval --project-directory "$workbench_dir/WeKnora" "${compose_args[@]}" up -d --no-build --pull never
docker compose -p weknora-structured -f "$project_dir/compose.yml" up -d --no-build --pull never
docker compose -p weknora-research -f "$workbench_dir/commodity-research/compose.yml" up -d --no-build --pull never
for ((attempt=1; attempt<=40; attempt++)); do
  if curl --fail --silent http://127.0.0.1:18081/health >/dev/null && curl --fail --silent http://127.0.0.1:18082/health >/dev/null && curl --fail --silent http://127.0.0.1:18080/structured/health >/dev/null && curl --fail --silent http://127.0.0.1:18080/research/healthz >/dev/null && curl --fail --silent http://127.0.0.1:18080/research/static/app.css >/dev/null && curl --fail --silent http://127.0.0.1:18080/research/static/app.js >/dev/null; then
    echo "WeKnora：http://127.0.0.1:18080"
    echo "结构化工作台：http://127.0.0.1:18080/platform/structured-data"
    echo "商品研究：http://127.0.0.1:18080/platform/commodity-research"
    exit 0
  fi
  sleep 1
done
echo "服务尚未就绪，请检查应用容器的日志。"
exit 1
