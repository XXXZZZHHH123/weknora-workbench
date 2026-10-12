#!/usr/bin/env bash
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
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
# Older servers may already have bootstrap installed; preserve a token on every update.
if [[ ! -e "$root/shared/research-service-token" ]]; then
  openssl rand -hex 32 > "$root/shared/research-service-token"
  chmod 600 "$root/shared/research-service-token"
fi
[[ -s "$root/shared/research-service-token" && -f "$root/shared/research-service-token" ]] || {
  echo 'Research service token must be a nonempty regular file.' >&2
  exit 1
}
compose=(docker compose --project-name weknora-workbench-prod --env-file "$root/shared/.env" --env-file "$release/release.env" -f "$release/compose.yml")
"${compose[@]}" config --quiet
# Docker may store images on a different, already-full filesystem.
docker_root="$(docker info --format '{{.DockerRootDir}}')"
free_kib="$(df -Pk "${docker_root:?Docker data directory unavailable}" | awk 'NR == 2 {print $4}')"
[[ "$free_kib" =~ ^[0-9]+$ && "$free_kib" -ge 15728640 ]] || {
  echo "Docker data filesystem needs at least 15 GiB free before deployment: $docker_root" >&2
  exit 1
}
transport=docker
if [[ -f "$root/shared/poller.json" ]]; then
  transport="$(jq -r '.image_transport // "docker"' "$root/shared/poller.json")"
fi
case "$transport" in
  docker) "${compose[@]}" pull --quiet ;;
  regctl)
    command -v regctl >/dev/null
    proxy="$(jq -r '.proxy // ""' "$root/shared/poller.json")"
    if [[ -n "$proxy" ]]; then export HTTP_PROXY="$proxy" HTTPS_PROXY="$proxy"; fi
    while IFS= read -r image; do
      if docker image inspect "$image" >/dev/null 2>&1; then
        echo "Image already available: $image"
        continue
      fi
      echo "Importing $image"
      regctl image export "$image" --platform linux/amd64 | docker load
    done < <("${compose[@]}" config --images)
    ;;
  *) echo 'Unsupported image transport' >&2; exit 1 ;;
esac

# Back up PostgreSQL before starting the new app (which may run migrations).
# Failed backups abort deployment; no service or volume is removed.
postgres_id="$("${compose[@]}" ps -q postgres)"
if [[ -n "$postgres_id" ]]; then
  backup="$(date -u +%Y%m%dT%H%M%SZ)-$release_id.pg.dump"
  # Variables belong to the container's environment, not this SSH shell.
  # shellcheck disable=SC2016
  "${compose[@]}" exec --user 0 -T postgres sh -c 'umask 077; pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc -f "/backups/$1.tmp" && mv "/backups/$1.tmp" "/backups/$1"' sh "$backup"
fi

# Snapshot every workspace SQLite DB using SQLite's backup API, so WAL commits
# are included. The secret is backed up separately with shared/.env.
research_id="$("${compose[@]}" ps -q commodity-research)"
if [[ -n "$research_id" ]]; then
  mkdir -p "$root/shared/backups"
  research_backup="$root/shared/backups/$(date -u +%Y%m%dT%H%M%SZ)-$release_id.research.tar.gz"
  if ! "${compose[@]}" exec -T commodity-research python /app/deploy/container/backup.py > "$research_backup.tmp"; then
    rm -f "$research_backup.tmp"
    echo 'Research backup failed; service update was aborted.' >&2
    exit 1
  fi
  mv "$research_backup.tmp" "$research_backup"
fi

if ! "${compose[@]}" up -d --no-build --pull never --wait --wait-timeout 300; then
  "${compose[@]}" ps
  echo "Release $release_id failed health checks; current pointer was not changed. Inspect container logs locally. Database migrations may already have run; follow the recovery instructions." >&2
  exit 1
fi

# A protected API must return 401, not a SPA fallback with HTTP 200.
status="$("${compose[@]}" exec -T app curl -sS -o /dev/null -w '%{http_code}' http://frontend/api/v1/auth/me)"
[[ "$status" == 401 ]] || { echo "API proxy probe failed: HTTP $status" >&2; exit 1; }
"${compose[@]}" exec -T frontend sh -c 'wget -q -O - http://127.0.0.1/structured/health | grep -q '"'"'"status":"ok"'"'"''
"${compose[@]}" exec -T frontend sh -c 'wget -q -O /dev/null http://127.0.0.1/research/healthz'
# HTML/bootstrap can be healthy while a prefixed StaticFiles mount is broken.
# Check styles/scripts and MIME types to reject both 404 and a SPA fallback.
for research_asset in app.css app.js tailwind.min.js; do
  asset_probe="$("${compose[@]}" exec -T app curl -sS -o /dev/null -w '%{http_code} %{content_type}' "http://frontend/research/static/$research_asset")"
  case "$research_asset:$asset_probe" in
    app.css:200\ text/css*|*.js:200\ text/javascript*|*.js:200\ application/javascript*) ;;
    *) echo "Research static proxy probe failed: $research_asset $asset_probe" >&2; exit 1 ;;
  esac
done
research_status="$("${compose[@]}" exec -T app curl -sS -o /dev/null -w '%{http_code}' http://frontend/research/api/board)"
[[ "$research_status" == 401 ]] || { echo "Research auth proxy probe failed: HTTP $research_status" >&2; exit 1; }
public_origin="$("${compose[@]}" exec -T structured-data python -c 'import os; print(os.environ.get("STRUCTURED_PUBLIC_ORIGIN", "").rstrip("/"))')"
if [[ -n "$public_origin" ]]; then
  authority="${public_origin#*://}"
  # $1 is expanded by the container shell, not the deployment shell.
  # shellcheck disable=SC2016
  "${compose[@]}" exec -T frontend sh -c 'wget -q -O - --header="Host: $1" http://127.0.0.1/structured/health | grep -q '"'"'"status":"ok"'"'"'' sh "$authority"
fi
if [[ -L "$root/current" ]]; then
  ln -sfn "$(readlink "$root/current")" "$root/previous"
fi
ln -sfn "$release" "$root/current.next"
mv -Tf "$root/current.next" "$root/current"
echo "Deployed $release_id"
