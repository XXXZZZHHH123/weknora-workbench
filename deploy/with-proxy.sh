#!/usr/bin/env bash
# Only the requested process and its children inherit these proxy variables.
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
[[ $# -gt 0 ]] || { echo 'usage: with-proxy.sh COMMAND [ARGS...]' >&2; exit 2; }
proxy=http://127.0.0.1:17891
bypass=localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,.local
export HTTP_PROXY="$proxy" HTTPS_PROXY="$proxy" ALL_PROXY="$proxy" NO_PROXY="$bypass"
export http_proxy="$proxy" https_proxy="$proxy" all_proxy="$proxy" no_proxy="$bypass"
exec "$@"
