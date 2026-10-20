#!/usr/bin/env bash
set -euo pipefail
project_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$project_root"
clean_build=0
check_memory=0
if [[ "$#" -gt 1 ]]; then
  echo "用法：bash scripts/build-local.sh [--clean|--check-memory]" >&2
  exit 2
fi
case "${1:-}" in
  "") ;;
  --clean) clean_build=1 ;;
  --check-memory) check_memory=1 ;;
  --help|-h)
    echo "用法：bash scripts/build-local.sh [--clean|--check-memory]"
    echo "--clean 使用全新 Go 编译缓存，强制编译全部依赖；前端每次重新检查类型并打包。"
    echo "--check-memory 仅检查内存预算，不启动构建或替换现有产物。"
    exit 0
    ;;
  *) echo "未知参数：$1" >&2; exit 2 ;;
esac

memory_guard="$project_root/scripts/build-memory-guard.py"
if [[ ! -x /usr/bin/python3 || ! -f "$memory_guard" ]]; then
  echo "缺少构建内存保护检查器，已停止；现有构建未替换。" >&2
  exit 1
fi
read_budget() {
  read -r -a build_budget <<< "$budget"
  if [[ "${#build_budget[@]}" -ne 10 ]]; then
    echo "内部构建预算缺失或无效，已停止。" >&2
    exit 1
  fi
  local value
  for value in "${build_budget[@]}"; do
    if [[ ! "$value" =~ ^-?[0-9]+$ ]]; then
      echo "内部构建预算格式无效，已停止。" >&2
      exit 1
    fi
  done
}
apply_budget_environment() {
  # Keep the checked options, then make the final heap ceiling explicit.
  local node_limit_suffix="--max-old-space-size=${build_budget[3]} --v8-pool-size=2"
  if [[ "${NODE_OPTIONS:-}" != "$node_limit_suffix" && "${NODE_OPTIONS:-}" != *" $node_limit_suffix" ]]; then
    export NODE_OPTIONS="${NODE_OPTIONS:+$NODE_OPTIONS }$node_limit_suffix"
  else
    export NODE_OPTIONS
  fi
  export GOMEMLIMIT="${build_budget[4]}MiB"
}
report_budget() {
  echo "构建内存预检通过：内存 ${build_budget[0]}MiB，回收阈值 ${build_budget[1]}MiB，swap ${build_budget[2]}MiB，Node 堆 ${build_budget[3]}MiB，Go ${build_budget[4]}MiB；尚未开始构建。"
}

if [[ "${WEKNORA_BUILD_LIMITED:-0}" != "1" ]]; then
  budget="$(/usr/bin/python3 "$memory_guard" plan)"
  read_budget
  apply_budget_environment
  if [[ "$check_memory" == "1" ]]; then
    report_budget
    exit 0
  fi
  # All native builds share a parent budget; a compiler failure stays in its scope.
  exec systemd-run --user --scope --quiet --unit="weknora-build-$BASHPID" \
    --slice=weknorabuild.slice \
    --property=MemoryAccounting=yes --property="MemoryHigh=${build_budget[1]}M" \
    --property="MemoryMax=${build_budget[0]}M" --property="MemorySwapMax=${build_budget[2]}M" \
    --property=OOMPolicy=kill --property=CPUQuota=200% \
    /usr/bin/env WEKNORA_BUILD_LIMITED=1 WEKNORA_BUILD_BUDGET="$budget" \
    NODE_OPTIONS="$NODE_OPTIONS" GOMEMLIMIT="$GOMEMLIMIT" \
    /usr/bin/bash "$project_root/scripts/build-local.sh" "$@"
fi

if [[ -z "${WEKNORA_BUILD_BUDGET:-}" ]]; then
  echo "受限构建缺少内存预算；环境标记不能代替实际限额，已停止。" >&2
  exit 1
fi
budget="$WEKNORA_BUILD_BUDGET"
read_budget
# Restrict only this build's descendants, preserving global crash-dump settings.
ulimit -c 0
printf '0\n' > /proc/self/coredump_filter
budget="$(/usr/bin/python3 "$memory_guard" verify "${build_budget[@]}")"
read_budget
apply_budget_environment
if [[ "$check_memory" == "1" ]]; then
  report_budget
  exit 0
fi
export PATH="$project_root/.build/tools/go/bin:$project_root/.build/tools/node-v24.14.0-linux-x64/bin:$PATH"
export GOPATH="$project_root/.build/go-path" GOMODCACHE="$project_root/.build/go-mod"
export GOCACHE="$project_root/.build/go-cache" GOTOOLCHAIN=local
export GOMAXPROCS="${GOMAXPROCS:-1}"
export CGO_ENABLED=1 CGO_CFLAGS=-Wno-deprecated-declarations
export C_INCLUDE_PATH="$project_root/.build/tools/sysroot/usr/include${C_INCLUDE_PATH:+:$C_INCLUDE_PATH}"
for required in .venv/bin/python WeKnora/frontend/node_modules/vite/bin/vite.js; do
  if [[ ! -f "$required" ]]; then
    echo "缺少构建依赖：$required" >&2
    exit 1
  fi
done
mkdir -p .build
# Serialise builds so separate terminals cannot each allocate the full build limit.
exec 9>"$project_root/.build/build.lock"
if ! flock -n 9; then
  echo "已有本地构建在运行，请等待它完成。" >&2
  exit 1
fi
staging="$(mktemp -d "$project_root/.build/rebuild.XXXXXX")"
trap 'rm -rf "$staging"' EXIT
go_build_args=()
if [[ "$clean_build" == "1" ]]; then
  export GOCACHE="$staging/go-cache"
  go_build_args=(-a)
  echo "全量重建：使用空的 Go 编译缓存"
fi
commit="$(git rev-parse HEAD)"
version="$(cat WeKnora/VERSION)"
build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
go_version="$(go env GOVERSION)"
export VITE_FRONTEND_COMMIT="$commit"
ldflags="-w -s -X github.com/Tencent/WeKnora/internal/handler.Version=$version -X github.com/Tencent/WeKnora/internal/handler.Edition=standard -X github.com/Tencent/WeKnora/internal/handler.CommitID=$commit -X github.com/Tencent/WeKnora/internal/handler.BuildTime=$build_time -X github.com/Tencent/WeKnora/internal/handler.GoVersion=$go_version -X google.golang.org/protobuf/reflect/protoregistry.conflictPolicy=warn"
echo "构建后端：$commit"
(cd WeKnora && go build "${go_build_args[@]}" -p "$GOMAXPROCS" -tags sqlite_fts5 -ldflags "$ldflags" -o "$staging/weknora-local" ./cmd/server)
echo "检查前端类型并构建"
(cd WeKnora/frontend && npm run type-check -- --force && npm run build -- --outDir "$staging/frontend-dist")
[[ -s "$staging/weknora-local" && -s "$staging/frontend-dist/index.html" ]]
mkdir -p "$project_root/.build/backups"
backup="$(mktemp -d "$project_root/.build/backups/$(date -u +%Y%m%dT%H%M%SZ).XXXXXX")"
[[ ! -f .build/weknora-local ]] || mv .build/weknora-local "$backup/weknora-local"
[[ ! -d WeKnora/frontend/dist ]] || mv WeKnora/frontend/dist "$backup/frontend-dist"
[[ ! -f .build/build-info.json ]] || mv .build/build-info.json "$backup/build-info.json"
mv "$staging/weknora-local" .build/weknora-local
mv "$staging/frontend-dist" WeKnora/frontend/dist
if [[ "$clean_build" == "1" ]]; then
  [[ ! -d .build/go-cache ]] || mv .build/go-cache "$backup/go-cache"
  mv "$GOCACHE" .build/go-cache
fi
.venv/bin/python - "$commit" "$build_time" "$backup" "$clean_build" <<'PY'
import hashlib, json, subprocess, sys
from pathlib import Path
def digest(path):
    with Path(path).open('rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()
Path('.build/build-info.json').write_text(json.dumps({
    'commit': sys.argv[1], 'build_time': sys.argv[2], 'previous_build': sys.argv[3],
    'clean_build': sys.argv[4] == '1', 'forced_type_check': True,
    'working_tree': subprocess.check_output(['git', 'status', '--short'], text=True).splitlines(),
    'backend_sha256': digest('.build/weknora-local'),
    'frontend_index_sha256': digest('WeKnora/frontend/dist/index.html'),
}, ensure_ascii=False, indent=2) + '\n')
PY
echo "构建完成，旧构建保存在 $backup"
echo "重启本地服务：bash scripts/stop-local.sh && bash scripts/start-local.sh"
