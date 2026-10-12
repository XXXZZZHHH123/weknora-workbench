#!/usr/bin/env bash
set -euo pipefail
project_root="$(cd "$(dirname "$0")/.." && pwd)"
for name in frontend commodity-research structured-data backend; do
  unit="weknora-workbench-$name"
  if systemctl --user is-active --quiet "$unit"; then
    systemctl --user stop "$unit"
    echo "$name 已停止"
  fi
  pid_file="$project_root/.build/run/$name.pid"
  if [[ -f "$pid_file" ]]; then
    rm -f "$pid_file"
  fi
done
