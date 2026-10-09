#!/usr/bin/env bash
set -euo pipefail
root="${1:?usage: bootstrap.sh ABSOLUTE_DEPLOY_DIR OWNER/REPO [HTTP_PROXY_URL]}"
repo="${2:?repository required}"
proxy="${3:-}"
[[ "$root" =~ ^/[a-zA-Z0-9_./-]+$ && "$repo" =~ ^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$ ]] || { echo 'Invalid directory or repository' >&2; exit 1; }
export PATH="$HOME/.local/bin:$PATH"
command -v jq >/dev/null
command -v openssl >/dev/null
command -v flock >/dev/null
docker info >/dev/null
docker compose version
umask 077
mkdir -p "$root"/{bin,releases,shared/structured-data,shared/backups}
script_dir="$(cd "$(dirname "$0")" && pwd)"
if [[ ! -e "$root/shared/.env" ]]; then
  cp "$script_dir/.env.example" "$root/shared/.env"
  for key in DB_PASSWORD REDIS_PASSWORD JWT_SECRET SYSTEM_SIGNING_KEY; do
    sed -i "s/^$key=$/$key=$(openssl rand -hex 32)/" "$root/shared/.env"
  done
  sed -i "s/^SYSTEM_AES_KEY=$/SYSTEM_AES_KEY=$(openssl rand -hex 16)/" "$root/shared/.env"
fi
if [[ ! -e "$root/shared/poller.json" ]]; then
  jq -n --arg repo "$repo" --arg proxy "$proxy" '{repository: $repo, proxy: $proxy}' > "$root/shared/poller.json"
fi
install -m 700 "$script_dir/poll.sh" "$root/bin/poll.sh"
echo "Prepared $root; existing configuration was preserved."
echo "Review shared/.env, authenticate Docker to GHCR if needed, then install the cron entry in deploy/README.md."
