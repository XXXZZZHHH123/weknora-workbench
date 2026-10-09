#!/usr/bin/env bash
set -euo pipefail
workbench_dir="$(cd "$(dirname "$0")" && pwd)"
exec bash "$workbench_dir/structured-data/scripts/start.sh" "$@"
