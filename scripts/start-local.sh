#!/usr/bin/env bash
set -euo pipefail
project_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$project_root"
export PATH="$project_root/.build/tools/node-v24.14.0-linux-x64/bin:$PATH"
for required in .build/weknora-local .venv/bin/structured-data .venv/bin/uvicorn commodity-research/src/tin/web/app.py WeKnora/frontend/dist/index.html WeKnora/docreader/.venv/bin/python scripts/check-local-docreader.py; do
  if [[ ! -f "$required" ]]; then
    echo "缺少运行文件：$required；请先按 README 构建。" >&2
    exit 1
  fi
done
mkdir -p .private/weknora/files .private/commodity-research .build/run logs
if [[ ! -f .private/research-service-token ]]; then
  .venv/bin/python - <<'PY'
import os, secrets
with open('.private/research-service-token', 'x', opener=lambda path, flags: os.open(path, flags, 0o600)) as output:
    output.write(secrets.token_urlsafe(48) + '\n')
PY
fi
if [[ ! -f .private/docreader-service-token ]]; then
  .venv/bin/python - <<'PY'
import os, secrets
with open('.private/docreader-service-token', 'x', opener=lambda path, flags: os.open(path, flags, 0o600)) as output:
    output.write(secrets.token_urlsafe(48) + '\n')
PY
fi
if [[ ! -f .private/local.env ]]; then
  umask 077
  .venv/bin/python - <<'PY'
from pathlib import Path
import secrets
Path('.private/local.env').write_text(
    'JWT_SECRET=' + secrets.token_hex(32) + '\n'
    'SYSTEM_AES_KEY=' + secrets.token_hex(16) + '\n'
    'STRUCTURED_TENANT_ID=1\n'
)
PY
fi
set -a
source .private/local.env
set +a
export DOCREADER_ADDR=127.0.0.1:50051
export GRPC_AUTH_TOKEN="${GRPC_AUTH_TOKEN:-$(<"$project_root/.private/docreader-service-token")}"
export PYTHONPATH="$project_root/WeKnora${PYTHONPATH:+:$PYTHONPATH}"
if ! WeKnora/docreader/.venv/bin/python -c 'import docreader.parser; import grpc_health.v1.health' >/dev/null; then
  echo "DocReader 解析依赖缺失；请按 README 安装 WeKnora/docreader 环境。" >&2
  exit 1
fi
if ! systemctl --user show-environment >/dev/null 2>&1; then
  echo "本地启动需要可用的 systemd 用户服务。" >&2
  exit 1
fi

service_ready() {
  if [[ "$1" == docreader ]]; then
    "$project_root/WeKnora/docreader/.venv/bin/python" "$project_root/scripts/check-local-docreader.py" >/dev/null 2>&1
  else
    curl --noproxy '*' -fsS --max-time 2 "$2" >/dev/null 2>&1
  fi
}

start_service() {
  local name="$1" health_url="$2"
  local unit="weknora-workbench-$name"
  local pid_file="$project_root/.build/run/$name.pid"
  if systemctl --user is-active --quiet "$unit"; then
    echo "$name 已在运行"
  else
    if service_ready "$name" "$health_url"; then
      echo "$health_url 已被其他服务占用，请确认后重试。" >&2
      return 1
    fi
    systemd-run --user --quiet --collect --unit="$unit" \
      --property=Restart=on-failure --property=RestartSec=3 \
      /usr/bin/bash "$project_root/scripts/run-local-service.sh" "$name"
  fi
  for attempt in $(seq 1 60); do
    if service_ready "$name" "$health_url"; then
      systemctl --user show "$unit" -p MainPID --value >"$pid_file"
      return 0
    fi
    sleep 1
  done
  echo "$name 启动失败，请查看 logs/$name.log" >&2
  return 1
}
start_service docreader grpc://127.0.0.1:50051
start_service backend http://127.0.0.1:18081/health
start_service structured-data http://127.0.0.1:18082/health
start_service commodity-research http://127.0.0.1:18083/healthz
start_service frontend http://127.0.0.1:18080/
echo "WeKnora：http://127.0.0.1:18080"
echo "表格工作台：http://127.0.0.1:18080/platform/structured-data"
echo "知识库：http://127.0.0.1:18080/platform/knowledge-bases"
