#!/usr/bin/env bash
set -euo pipefail
project_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$project_root"
set -a
source .private/local.env
set +a
export PATH="$project_root/.build/tools/node-v24.14.0-linux-x64/bin:$PATH"
export DB_DRIVER=sqlite DB_PATH="$project_root/.private/weknora/weknora.db"
export RETRIEVE_DRIVER=sqlite STORAGE_TYPE=local
export LOCAL_STORAGE_BASE_DIR="$project_root/.private/weknora/files"
export STREAM_MANAGER_TYPE=memory GIN_MODE=release LOG_LEVEL=info
export SERVER_HOST=127.0.0.1 SERVER_PORT=18081 MAX_FILE_SIZE_MB=100
export SSRF_WHITELIST_EXTRA=127.0.0.1,localhost
export WEKNORA_SANDBOX_MODE=disabled NEO4J_ENABLE=false
export WEKNORA_API_URL=http://127.0.0.1:18081
export VITE_DEV_PROXY_TARGET=http://127.0.0.1:18081
export VITE_STRUCTURED_PROXY_TARGET=http://127.0.0.1:18082
export VITE_RESEARCH_PROXY_TARGET=http://127.0.0.1:18083
export COMMODITY_RESEARCH_URL=http://127.0.0.1:18083
export STRUCTURED_DATA_URL=http://127.0.0.1:18082
export COMMODITY_RESEARCH_TOKEN_FILE="$project_root/.private/research-service-token"
export STRUCTURED_SERVICE_TOKEN_FILE="$COMMODITY_RESEARCH_TOKEN_FILE"
export NO_PROXY=127.0.0.1,localhost
unset REDIS_ADDR DOCREADER_ADDR
case "${1:-}" in
  backend)
    cd "$project_root/WeKnora"
    exec "$project_root/.build/weknora-local" >>"$project_root/logs/backend.log" 2>&1
    ;;
  structured-data)
    exec "$project_root/.venv/bin/structured-data" --data-dir "$project_root/structured-data/data" serve --host 127.0.0.1 --port 18082 >>"$project_root/logs/structured-data.log" 2>&1
    ;;
  commodity-research)
    export TIN_WEKNORA_API_URL="$WEKNORA_API_URL" TIN_PUBLIC_PREFIX=/research
    export TIN_DATA_DIR="$project_root/.private/commodity-research"
    export TIN_DATABASE_URL="sqlite:///$TIN_DATA_DIR/standalone.db"
    export TIN_EXPORTS_DIR="$TIN_DATA_DIR/exports" TIN_STAGING_DIR="$TIN_DATA_DIR/import_staging"
    export TIN_SERVICE_TOKEN_FILE="$COMMODITY_RESEARCH_TOKEN_FILE"
    export TIN_STRUCTURED_DATA_URL="$STRUCTURED_DATA_URL" TIN_REFRESH_SCHEDULER_ENABLED=false
    cd "$project_root/commodity-research"
    exec "$project_root/.venv/bin/uvicorn" tin.web.app:app --host 127.0.0.1 --port 18083 >>"$project_root/logs/commodity-research.log" 2>&1
    ;;
  frontend)
    cd "$project_root/WeKnora/frontend"
    exec node node_modules/vite/bin/vite.js preview --host 127.0.0.1 --port 18080 --strictPort >>"$project_root/logs/frontend.log" 2>&1
    ;;
  *) echo "请指定 backend、structured-data、commodity-research 或 frontend。" >&2; exit 1 ;;
esac
