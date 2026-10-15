#!/usr/bin/env bash
# Shared leases keep concurrent callers online; no remote polling is scheduled.
set -euo pipefail
umask 077
export PATH="$HOME/.local/bin:$PATH"
script_dir="$(cd "$(dirname "$0")" && pwd -P)"
stage="$HOME/.local/share/weknora-proxy"
container=weknora-egress-proxy
mkdir -p "$stage"
chmod 700 "$stage"

stop_if_idle() {
  exec 6>"$stage/sessions.lock"
  if flock -xn 6; then
    if [[ "$(docker inspect --format '{{.State.Status}}' "$container" 2>/dev/null || true)" == running ]]; then
      # Preserve existing connections, including a client launched before leases
      # were installed. This controller request stays entirely on loopback.
      if [[ -s "$stage/config/control.curl" ]]; then
        local active
        active="$(curl --noproxy '*' --config "$stage/config/control.curl" -fsS --max-time 3 \
          http://127.0.0.1:17991/connections | jq -er '.connections | length')" || return 1
        [[ "$active" == 0 ]] || return 1
      fi
      docker stop "$container" >/dev/null
      echo 'Proxy automatically stopped: no active callers.' >&2
    fi
    return 0
  fi
  return 1
}

if [[ "${1:-}" == --watch ]]; then
  # Replace inherited descriptors so the watcher itself never holds a lease.
  exec 7>"$stage/watcher.lock"
  flock -n 7 || exit 0
  while true; do
    exec 9>"$stage/lifecycle.lock"
    flock 9
    if stop_if_idle; then exit 0; fi
    exec 6>&-
    flock -u 9
    exec 9>&-
    sleep 1
  done
fi

[[ $# -gt 0 ]] || { echo 'usage: proxy-session.sh COMMAND [ARGS...]' >&2; exit 2; }
cleanup() {
  local result=$?
  trap - EXIT
  exec 6>&-
  exec 9>"$stage/lifecycle.lock"
  flock 9
  stop_if_idle || true
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

exec 9>"$stage/lifecycle.lock"
flock 9
# poll.sh/deploy.sh use descriptors 8/9; keep this lease on a separate one.
exec 6>"$stage/sessions.lock"
flock -s 6
bash "$script_dir/linux-proxy.sh" ensure-started >&2
# Do not keep an unused proxy running after a machine reboot.
docker update --restart on-failure:3 "$container" >/dev/null
nohup bash "$0" --watch 6>&- 9>&- </dev/null >>"$stage/auto-stop.log" 2>&1 &
flock -u 9
exec 9>&-
WEKNORA_PROXY_SESSION=1 "$@"
