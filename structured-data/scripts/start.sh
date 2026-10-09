#!/bin/bash
set -euo pipefail
project_dir="$(cd "$(dirname "$0")/.." && pwd)"
workbench_dir="$(dirname "$project_dir")"
if ! docker info >/dev/null 2>&1; then
  echo "Docker 未运行，请先打开 Docker Desktop，再执行本脚本。"
  exit 1
fi
# Keep project names stable: they own the existing database volumes and network.
docker compose -p weknora-eval --project-directory "$workbench_dir/WeKnora" -f "$workbench_dir/WeKnora/docker-compose.yml" -f "$workbench_dir/compose.test.yml" up -d --no-build --pull never
docker compose -p weknora-structured -f "$project_dir/compose.yml" up -d --no-build --pull never
for attempt in $(seq 1 40); do
  if curl --fail --silent http://127.0.0.1:18081/health >/dev/null && curl --fail --silent http://127.0.0.1:18082/health >/dev/null && curl --fail --silent http://127.0.0.1:18080/structured/health >/dev/null; then
    echo "WeKnora：http://127.0.0.1:18080"
    echo "结构化工作台：http://127.0.0.1:18080/platform/structured-data"
    exit 0
  fi
  sleep 1
done
echo "服务尚未就绪，请检查两个应用容器的日志。"
exit 1
