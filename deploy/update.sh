#!/usr/bin/env bash
# Run once on the server to deploy the latest published production release.
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
script_dir="$(cd "$(dirname "$0")" && pwd -P)"
root="$(cd "$script_dir/.." && pwd -P)"
if [[ $# -gt 0 && "$1" != --retry-failed ]]; then
  root="$1"
  shift
fi
if [[ $# -gt 1 || ( $# -eq 1 && "$1" != --retry-failed ) ]]; then
  echo 'usage: update.sh [ABSOLUTE_DEPLOY_DIR] [--retry-failed]' >&2
  exit 2
fi
[[ "$root" =~ ^/[a-zA-Z0-9_./-]+$ && -d "$root/shared" ]] || { echo 'Invalid deployment directory or missing server setup' >&2; exit 1; }
umask 077
# pipefail preserves the updater's failure status while displaying and saving logs.
if [[ "$(jq -r '.proxy // ""' "$root/shared/poller.json")" == http://127.0.0.1:17891 ]]; then
  bash "$script_dir/proxy-session.sh" bash "$script_dir/poll.sh" "$root" "$@" 2>&1 | tee -a "$root/shared/deploy.log"
else
  bash "$script_dir/poll.sh" "$root" "$@" 2>&1 | tee -a "$root/shared/deploy.log"
fi
