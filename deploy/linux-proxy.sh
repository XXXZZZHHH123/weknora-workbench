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

# Network tools use short leases; a status query never wakes an idle proxy.
case "$action" in
  check|nodes|select|latency|activate)
    if [[ "${WEKNORA_PROXY_SESSION:-}" != 1 ]]; then
      exec bash "$script_dir/proxy-session.sh" bash "$0" "$@"
    fi ;;
esac

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
    docker run -d --name "$container" --label weknora.egress-proxy=true --restart on-failure:3 \
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

ensure_started() {
  if [[ "$(docker inspect --format '{{.State.Status}}' "$container" 2>/dev/null || true)" == running ]]; then
    curl --noproxy '*' --config "$stage/config/control.curl" -fsS --max-time 5 http://127.0.0.1:17991/version >/dev/null
  else
    start_proxy
  fi
}

check_proxy() {
  local github ghcr token auth headers body limited=false github_bad=false
  auth="$(mktemp "$stage/.github-auth.XXXXXX")"
  headers="$auth.headers"
  body="$auth.json"
  if [[ -s "$HOME/weknora-workbench/shared/github-token" ]]; then
    token="$(cat "$HOME/weknora-workbench/shared/github-token")"
    [[ "$token" =~ ^[a-zA-Z0-9_]+$ ]] || { rm "$auth"; echo 'Invalid server GitHub token format.' >&2; return 1; }
    printf 'header = "Authorization: Bearer %s"\n' "$token" > "$auth"
    unset token
  fi
  github="$(curl --noproxy '' --proxy "$proxy" --config "$auth" -sS --connect-timeout 10 --max-time 30 -D "$headers" -o "$body" -w '%{http_code}' https://api.github.com)" || { rm -f "$auth" "$headers" "$body"; return 1; }
  rm "$auth"
  if [[ "$github" == 403 || "$github" == 429 ]]; then
    if grep -iq '^x-ratelimit-remaining: 0' "$headers" || jq -e '.message // "" | ascii_downcase | contains("rate limit")' "$body" >/dev/null 2>&1; then
      limited=true
      echo 'GitHub is reachable, but its API rate limit is exhausted; this is not a proxy disconnection.' >&2
    fi
  fi
  rm "$headers" "$body"
  ghcr="$(curl --noproxy '' --proxy "$proxy" -sS --connect-timeout 10 --max-time 30 -o /dev/null -w '%{http_code}' https://ghcr.io/v2/)" || return 1
  echo "GitHub API: HTTP $github; GHCR: HTTP $ghcr (401 is the normal registry authentication challenge)."
  if [[ "$github" != 200 && "$limited" != true ]]; then
    github_bad=true
    echo "GitHub request failed: HTTP $github; verify authentication or access restrictions." >&2
  fi
  [[ "$ghcr" == 401 ]] || { echo 'GHCR check failed; deployment settings have not changed.' >&2; return 1; }
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
  [[ "$github_bad" == false && "$limited" == false ]]
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
  start|ensure-started) ensure_started ;;
  check) check_proxy ;;
  select)
    node="${2:?usage: linux-proxy.sh select NODE_NAME}"
    jq -n --arg name "$node" '{name:$name}' | curl --noproxy '*' \
      --config "$stage/config/control.curl" -fsS --max-time 10 -X PUT \
      -H 'Content-Type: application/json' --data-binary @- http://127.0.0.1:17991/proxies/PROXY
    echo 'Node selected; run check to verify access.' ;;
  activate) activate_deploy ;;
  status)
    if ! state="$(docker inspect --format '{{.State.Status}}' "$container" 2>/dev/null)"; then
      echo 'Proxy container has not been created.'; exit 0
    fi
    echo "Proxy status: $state"
    if [[ "$state" != running ]]; then
      echo 'Stopped: update.sh / with-proxy.sh automatically start it when needed.'; exit 0
    fi
    group="$(curl --noproxy '*' --config "$stage/config/control.curl" -fsS --max-time 5 http://127.0.0.1:17991/proxies/PROXY | jq -er .now)"
    if [[ "$group" == AUTO ]]; then
      group="$(curl --noproxy '*' --config "$stage/config/control.curl" -fsS --max-time 5 http://127.0.0.1:17991/proxies/AUTO | jq -er .now)"
    fi
    printf 'Current node: %s\n' "$group"
    curl --noproxy '*' --config "$stage/config/control.curl" -fsS --max-time 5 \
      http://127.0.0.1:17991/connections | jq '{upload_total_bytes: .uploadTotal, download_total_bytes: .downloadTotal}'
    echo 'Counters are runtime statistics, not the subscription billing statement.' ;;
  nodes)
    curl --noproxy '*' --config "$stage/config/control.curl" -fsS --max-time 5 \
      http://127.0.0.1:17991/proxies/PROXY | jq '{selected: .now, nodes: .all}' ;;
  latency)
    echo 'Measuring HTTPS delay to the connectivity test endpoint; this does not measure download bandwidth.'
    node_list="$(mktemp "$stage/.nodes.XXXXXX")"
    delays="$(mktemp "$stage/.delays.XXXXXX")"
    if ! curl --noproxy '*' --config "$stage/config/control.curl" -fsS --max-time 5 \
      -o "$node_list" http://127.0.0.1:17991/proxies/PROXY || \
      ! curl --noproxy '*' --config "$stage/config/control.curl" -fsS --max-time 45 \
      --get --data-urlencode url=https://www.gstatic.com/generate_204 --data-urlencode timeout=5000 \
      -o "$delays" http://127.0.0.1:17991/group/PROXY/delay; then
      rm "$node_list" "$delays"; exit 1
    fi
    # The controller omits failed nodes; include them explicitly as timeouts.
    jq -r --slurpfile group "$node_list" '. as $delays | $group[0].all |
      map({key:., value:($delays[.] // 0)}) |
      sort_by(if .value > 0 then .value else 999999 end)[] |
      if .value > 0 then "\(.value) ms\t\(.key)" else "timeout\t\(.key)" end' "$delays"
    rm "$node_list" "$delays" ;;
  stop)
    exec 9>"$stage/lifecycle.lock"; flock 9
    exec 8>"$stage/sessions.lock"
    flock -xn 8 || { echo 'Proxy is in use by Codex/deployment; wait for callers to finish.' >&2; exit 1; }
    docker stop "$container"
    echo 'Proxy stopped; configuration was preserved. update.sh / with-proxy.sh start it when needed.' ;;
  restore-deploy)
    shared=/home/codexuser/weknora-workbench/shared
    [[ -s "$stage/deployment.before.json" ]] || { echo 'No deployment backup exists.' >&2; exit 1; }
    exec 9>"$shared/deploy.lock"; flock -n 9
    # shellcheck disable=SC2016
    jq --slurpfile before "$stage/deployment.before.json" '.proxy=$before[0].proxy | if $before[0] | has("image_transport") then .image_transport=$before[0].image_transport else del(.image_transport) end' "$shared/poller.json" > "$shared/poller.json.tmp"
    mv "$shared/poller.json.tmp" "$shared/poller.json"
    rm "$stage/deployment.before.json"
    echo 'Previous deployment proxy settings restored; application containers were not changed.' ;;
  *) echo 'usage: linux-proxy.sh {install|configure|start|check|activate|status|nodes|latency|select NODE|restore-deploy|stop}' >&2; exit 2 ;;
esac
