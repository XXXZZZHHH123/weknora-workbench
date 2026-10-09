#!/usr/bin/env bash
# CentOS 7-compatible pull deployment: no Python or Actions runner required.
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
root="${1:?usage: poll.sh ABSOLUTE_DEPLOY_DIR [--retry-failed]}"
[[ "$root" =~ ^/[a-zA-Z0-9_./-]+$ ]] || exit 1
root="$(cd "$root" && pwd -P)"
shared="$root/shared"
[[ ! -e "$shared/PAUSED" ]] || exit 0
umask 077
exec 8>"$shared/poll.lock"
flock -n 8 || exit 0
repo="$(jq -er .repository "$shared/poller.json")"
[[ "$repo" =~ ^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$ ]] || exit 1
proxy="$(jq -r '.proxy // ""' "$shared/poller.json")"
curl_opts=(--silent --show-error --connect-timeout 15 --max-time 120 --retry 2)
[[ -z "$proxy" ]] || curl_opts+=(--proxy "$proxy")
temp="$(mktemp -d "$root/.poll.XXXXXX")"
trap 'rm -rf "$temp"' EXIT
touch "$temp/auth.conf"
if [[ -s "$shared/github-token" ]]; then
  token="$(cat "$shared/github-token")"
  [[ "$token" =~ ^[a-zA-Z0-9_]+$ ]] || { echo 'Invalid GitHub token format' >&2; exit 1; }
  printf 'header = "Authorization: Bearer %s"\n' "$token" > "$temp/auth.conf"
  unset token
fi
api="https://api.github.com/repos/$repo"
code="$(curl "${curl_opts[@]}" --config "$temp/auth.conf" -H 'Accept: application/vnd.github+json' -o "$temp/latest.json" -w '%{http_code}' "$api/releases/latest")"
[[ "$code" != 404 ]] || exit 0
[[ "$code" == 200 ]] || { echo "Release lookup failed: HTTP $code" >&2; exit 1; }
release_id="$(jq -er 'select(.draft == false and .prerelease == false) | .tag_name' "$temp/latest.json")"
[[ "$release_id" =~ ^production-[a-f0-9]{40}-[0-9]+-[0-9]+$ ]] || { echo 'Latest release is not a production deployment' >&2; exit 1; }
if [[ -L "$root/current" && "$(basename "$(readlink "$root/current")")" == "$release_id" ]]; then
  exit 0
fi
if [[ -f "$shared/failed-release" && "$(cat "$shared/failed-release")" == "$release_id" && "${2:-}" != --retry-failed ]]; then
  exit 0
fi

download_asset() {
  local name="$1" url status location
  url="$(jq -er --arg name "$name" '[.assets[] | select(.name == $name)] | select(length == 1) | .[0].url' "$temp/latest.json")"
  [[ "$url" == "$api/releases/assets/"* && "${url##*/}" =~ ^[0-9]+$ ]] || return 1
  # Do not forward Authorization on redirects, even on CentOS's older curl.
  status="$(curl "${curl_opts[@]}" --config "$temp/auth.conf" -H 'Accept: application/octet-stream' -D "$temp/headers" -o "$temp/$name" -w '%{http_code}' "$url")"
  if [[ "$status" == 302 ]]; then
    location="$(sed -n 's/^[Ll]ocation: //p' "$temp/headers" | tr -d '\r')"
    [[ "$location" == https://release-assets.githubusercontent.com/* || "$location" == https://objects.githubusercontent.com/* ]] || return 1
    curl "${curl_opts[@]}" --fail -o "$temp/$name" "$location"
  else
    [[ "$status" == 200 ]] || return 1
  fi
}
download_asset deployment.tar.gz
download_asset deployment.tar.gz.sha256
(cd "$temp" && sha256sum --check --status deployment.tar.gz.sha256)
# Only these three regular files may be extracted. Reject links and paths.
[[ "$(tar -tzf "$temp/deployment.tar.gz" | LC_ALL=C sort)" == $'compose.yml\ndeploy.sh\nrelease.env' ]] || { echo 'Unexpected release archive members' >&2; exit 1; }
[[ "$(tar -tvzf "$temp/deployment.tar.gz" | cut -c1 | LC_ALL=C sort -u)" == '-' ]] || { echo 'Release archive contains non-regular files' >&2; exit 1; }
release="$root/releases/$release_id"
if [[ ! -d "$release" ]]; then
  mkdir "$temp/release"
  tar -xzf "$temp/deployment.tar.gz" --no-same-owner -C "$temp/release"
  mv "$temp/release" "$release"
fi
echo "$(date -u +%FT%TZ) Deploying $release_id"
if bash "$release/deploy.sh" "$root" "$release_id"; then
  rm -f "$shared/failed-release"
else
  printf '%s\n' "$release_id" > "$shared/failed-release"
  echo 'Deployment failed; this version will not be retried until --retry-failed is used or a new release is published.' >&2
  exit 1
fi
