#!/usr/bin/env bash
# User-managed proxy in a restricted container, without changing host networking.
set -euo pipefail
umask 077
export PATH="$HOME/.local/bin:$PATH"
stage="$HOME/.local/share/weknora-proxy"
script_dir="$(cd "$(dirname "$0")" && pwd -P)"
container=weknora-egress-proxy
proxy=http://127.0.0.1:17891
action="${1:-status}"
mkdir -p "$stage"/{runtime,config,data}
chmod 700 "$stage" "$stage/runtime" "$stage/config" "$stage/data"
image="$(docker inspect --format '{{.Image}}' weknora-workbench-prod-structured-data-1)"
helper=(docker run --rm -i --network none --read-only --cap-drop ALL --security-opt no-new-privileges
  --user "$(id -u):$(id -g)" --mount "type=bind,source=$stage,target=/state"
  --entrypoint python "$image")

install_binary() {
  local version=v1.19.32 expected=ba3ce607747a07f948fc35780e108a4a7c7f552a38b9bd4d115f313ebcb89c20
  if [[ -f "$stage/runtime/mihomo.binary.sha256" ]]; then
    (cd "$stage/runtime" && sha256sum --check --status mihomo.binary.sha256)
    "$stage/runtime/mihomo" -v
    return 0
  fi
  curl --noproxy '*' --fail --silent --show-error --location --connect-timeout 10 --max-time 120 \
    "https://github.com/MetaCubeX/mihomo/releases/download/$version/mihomo-linux-amd64-compatible-$version.gz" \
    -o "$stage/runtime/mihomo.gz.part"
  printf '%s  %s\n' "$expected" "$stage/runtime/mihomo.gz.part" | sha256sum --check --status
  gzip -dc "$stage/runtime/mihomo.gz.part" > "$stage/runtime/mihomo.tmp"
  chmod 700 "$stage/runtime/mihomo.tmp"
  "$stage/runtime/mihomo.tmp" -v
  mv "$stage/runtime/mihomo.tmp" "$stage/runtime/mihomo"
  (cd "$stage/runtime" && sha256sum mihomo > mihomo.binary.sha256)
  rm "$stage/runtime/mihomo.gz.part"
}

start_proxy() {
  [[ -x "$stage/runtime/mihomo" && -s "$stage/config/config.json" ]] || { echo 'Run install and configure first.' >&2; return 1; }
  if ! docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
    --user "$(id -u):$(id -g)" --mount "type=bind,source=$stage,target=/proxy" \
    --entrypoint /proxy/runtime/mihomo "$image" -t -d /proxy/data -f /proxy/config/config.json \
    > "$stage/config-check.log" 2>&1; then
    echo "Proxy config rejected; private diagnostic log: $stage/config-check.log" >&2; return 1
  fi
  if docker container inspect "$container" >/dev/null 2>&1; then
    [[ "$(docker inspect --format '{{index .Config.Labels "weknora.egress-proxy"}}' "$container")" == true ]] || { echo 'Container name already belongs to another service.' >&2; return 1; }
    docker restart "$container" >/dev/null
  else
    if netstat -lnt | awk '$4 ~ /:(17891|17991)$/ {found=1} END {exit !found}'; then
      echo 'Proxy port 17891 or controller port 17991 is occupied.' >&2; return 1
    fi
    docker run -d --name "$container" --label weknora.egress-proxy=true --restart unless-stopped \
      --network host --read-only --cap-drop ALL --security-opt no-new-privileges \
      --user "$(id -u):$(id -g)" --memory 384m --cpus 1 --pids-limit 128 \
      --log-opt max-size=2m --log-opt max-file=2 \
      --mount "type=bind,source=$stage/runtime,target=/proxy/runtime,readonly" \
      --mount "type=bind,source=$stage/config,target=/proxy/config,readonly" \
      --mount "type=bind,source=$stage/data,target=/proxy/data" \
      --entrypoint /proxy/runtime/mihomo "$image" -d /proxy/data -f /proxy/config/config.json >/dev/null
  fi
  for (( attempt=0; attempt<30; attempt++ )); do
    if curl --noproxy '*' --config "$stage/config/control.curl" -fsS --max-time 2 \
      http://127.0.0.1:17991/version >/dev/null 2>&1; then
      echo "Proxy running at $proxy (only loopback, no NET_ADMIN capability)."; return 0
    fi
    sleep 1
  done
  echo 'Proxy did not become ready; deployment settings have not changed.' >&2; return 1
}

check_proxy() {
  local github ghcr
  github="$(curl --noproxy '' --proxy "$proxy" -sS --connect-timeout 10 --max-time 30 -o /dev/null -w '%{http_code}' https://api.github.com)" || return 1
  ghcr="$(curl --noproxy '' --proxy "$proxy" -sS --connect-timeout 10 --max-time 30 -o /dev/null -w '%{http_code}' https://ghcr.io/v2/)" || return 1
  echo "GitHub API: HTTP $github; GHCR: HTTP $ghcr (401 is the normal registry authentication challenge)."
  [[ "$github" == 200 && "$ghcr" == 401 ]] || { echo 'GitHub/GHCR check failed; deployment settings have not changed.' >&2; return 1; }
  # CentOS 7 curl/NSS can fail TLS handshakes which work with modern TLS clients.
  # Use the existing Python/OpenSSL runtime without changing any host TLS packages.
  docker run --rm --network host --read-only --cap-drop ALL --security-opt no-new-privileges \
    --user "$(id -u):$(id -g)" --entrypoint python "$image" -c '
import sys, urllib.request, urllib.error
opener=urllib.request.build_opener(urllib.request.ProxyHandler({"http":sys.argv[1],"https":sys.argv[1]}))
targets=[("OpenAI API (no credentials)","https://api.openai.com/v1/models",401),
         ("OpenAI login discovery","https://auth.openai.com/.well-known/openid-configuration",200),
         ("Codex endpoint (GET probe)","https://chatgpt.com/backend-api/codex/responses",405)]
for name,url,expected in targets:
    try:
        with opener.open(url,timeout=20) as response: status=response.status
    except urllib.error.HTTPError as exc: status=exc.code
    except (urllib.error.URLError,TimeoutError,OSError): status=0
    print(f"{name}: HTTP {status}",flush=True)
    if status != expected: print("Access needs further verification; this does not confirm a logged-in Codex session.",flush=True)
' "$proxy"
}

activate_deploy() {
  check_proxy
  local shared=/home/codexuser/weknora-workbench/shared
  exec 9>"$shared/deploy.lock"
  flock -n 9 || { echo 'A deployment is active; retry activation later.' >&2; return 1; }
  if [[ ! -e "$stage/deployment.before.json" ]]; then
    cp "$shared/poller.json" "$stage/deployment.before.json"
  fi
  jq --arg proxy "$proxy" '.proxy=$proxy | .image_transport="regctl"' "$shared/poller.json" > "$shared/poller.json.tmp"
  chmod 600 "$shared/poller.json.tmp"
  mv "$shared/poller.json.tmp" "$shared/poller.json"
  echo 'Manual deployment downloads now use the independent Linux proxy. Docker daemon and running apps were not changed.'
}

case "$action" in
  install) install_binary ;;
  configure)
    [[ -x "$stage/runtime/mihomo" ]] || { echo 'Run install first.' >&2; exit 1; }
    read -rs -p 'Clash/Mihomo subscription HTTPS URL (hidden): ' subscription
    printf '\n'
    printf '%s' "$subscription" | "${helper[@]}" -c "$(cat "$script_dir/linux-proxy-config.py")" /state
    unset subscription
    start_proxy
    activate_deploy
    ;;
  start) start_proxy ;;
  check) check_proxy ;;
  select)
    node="${2:?usage: linux-proxy.sh select NODE_NAME}"
    jq -n --arg name "$node" '{name:$name}' | curl --noproxy '*' \
      --config "$stage/config/control.curl" -fsS --max-time 10 -X PUT \
      -H 'Content-Type: application/json' --data-binary @- http://127.0.0.1:17991/proxies/PROXY
    echo 'Node selected; run check to verify access.' ;;
  activate) activate_deploy ;;
  status)
    docker ps -a --filter name="^/$container$" --format '{{.Names}} {{.Status}}'
    if [[ -s "$stage/config/control.curl" ]]; then
      curl --noproxy '*' --config "$stage/config/control.curl" -fsS --max-time 5 \
        http://127.0.0.1:17991/proxies/PROXY | jq '{selected: .now, nodes: .all}'
    fi ;;
  stop)
    [[ -e "$stage/deployment.before.json" ]] && { echo 'Restore deployment proxy settings before stopping: linux-proxy.sh restore-deploy' >&2; exit 1; }
    docker stop "$container" ;;
  restore-deploy)
    shared=/home/codexuser/weknora-workbench/shared
    [[ -s "$stage/deployment.before.json" ]] || { echo 'No deployment backup exists.' >&2; exit 1; }
    exec 9>"$shared/deploy.lock"; flock -n 9
    # shellcheck disable=SC2016
    jq --slurpfile before "$stage/deployment.before.json" '.proxy=$before[0].proxy | if $before[0] | has("image_transport") then .image_transport=$before[0].image_transport else del(.image_transport) end' "$shared/poller.json" > "$shared/poller.json.tmp"
    mv "$shared/poller.json.tmp" "$shared/poller.json"
    rm "$stage/deployment.before.json"
    echo 'Previous deployment proxy settings restored; application containers were not changed.' ;;
  *) echo 'usage: linux-proxy.sh {install|configure|start|check|activate|status|select NODE|restore-deploy|stop}' >&2; exit 2 ;;
esac
