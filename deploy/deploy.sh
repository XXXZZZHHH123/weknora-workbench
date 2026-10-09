#!/usr/bin/env bash
set -euo pipefail
root="${1:?usage: deploy.sh ABSOLUTE_DEPLOY_DIR RELEASE_ID}"
release_id="${2:?release ID required}"
[[ "$root" =~ ^/[a-zA-Z0-9_./-]+$ && "$release_id" =~ ^production-[a-f0-9]{40}-[0-9]+-[0-9]+$ ]] || { echo 'Invalid deployment arguments' >&2; exit 1; }
root="$(cd "$root" && pwd -P)"
release="$root/releases/$release_id"
export DEPLOY_SHARED_DIR="$root/shared"
umask 077
exec 9>"$root/shared/deploy.lock"
flock -n 9 || { echo 'Another deployment is running' >&2; exit 1; }
[[ -f "$root/shared/.env" && -f "$release/release.env" && -f "$release/compose.yml" ]] || { echo 'Missing server configuration or release files' >&2; exit 1; }
compose=(docker compose --project-name weknora-workbench-prod --env-file "$root/shared/.env" --env-file "$release/release.env" -f "$release/compose.yml")
"${compose[@]}" config --quiet
# Docker may store images on a different, already-full filesystem.
docker_root="$(docker info --format '{{.DockerRootDir}}')"
free_kib="$(df -Pk "${docker_root:?Docker data directory unavailable}" | awk 'NR == 2 {print $4}')"
[[ "$free_kib" =~ ^[0-9]+$ && "$free_kib" -ge 15728640 ]] || {
  echo "Docker data filesystem needs at least 15 GiB free before deployment: $docker_root" >&2
  exit 1
}
"${compose[@]}" pull --quiet

# Back up PostgreSQL before starting the new app (which may run migrations).
# Failed backups abort deployment; no service or volume is removed.
postgres_id="$("${compose[@]}" ps -q postgres)"
if [[ -n "$postgres_id" ]]; then
  backup="$root/shared/backups/$(date -u +%Y%m%dT%H%M%SZ)-$release_id.pg.dump"
  # Variables belong to the container's environment, not this SSH shell.
  # shellcheck disable=SC2016
  "${compose[@]}" exec -T postgres sh -c 'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$backup.tmp"
  mv "$backup.tmp" "$backup"
fi

if ! "${compose[@]}" up -d --no-build --wait --wait-timeout 300; then
  "${compose[@]}" ps
  echo "Release $release_id failed health checks; current pointer was not changed. Inspect container logs locally. Database migrations may already have run; follow the recovery instructions." >&2
  exit 1
fi

# A protected API must return 401, not a SPA fallback with HTTP 200.
status="$("${compose[@]}" exec -T app curl -sS -o /dev/null -w '%{http_code}' http://frontend/api/v1/auth/me)"
[[ "$status" == 401 ]] || { echo "API proxy probe failed: HTTP $status" >&2; exit 1; }
"${compose[@]}" exec -T frontend sh -c 'wget -q -O - http://127.0.0.1/structured/health | grep -q '"'"'"status":"ok"'"'"''
if [[ -L "$root/current" ]]; then
  ln -sfn "$(readlink "$root/current")" "$root/previous"
fi
ln -sfn "$release" "$root/current.next"
mv -Tf "$root/current.next" "$root/current"
echo "Deployed $release_id"
