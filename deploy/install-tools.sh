#!/usr/bin/env bash
# Install verified user-scoped binaries; no root or system package changes.
set -euo pipefail
proxy="${1:-}"
[[ "$(uname -m)" == x86_64 && "$(uname -s)" == Linux ]] || { echo 'This installer supports Linux x86_64' >&2; exit 1; }
umask 077
temp="$(mktemp -d)"
trap 'rm -rf "$temp"' EXIT
opts=(--fail --silent --show-error --location --retry 2 --connect-timeout 15 --max-time 180)
[[ -z "$proxy" ]] || opts+=(--proxy "$proxy")
compose_version=v2.39.2
jq_version=jq-1.7.1
curl "${opts[@]}" "https://github.com/docker/compose/releases/download/$compose_version/docker-compose-linux-x86_64" -o "$temp/docker-compose"
curl "${opts[@]}" "https://github.com/docker/compose/releases/download/$compose_version/docker-compose-linux-x86_64.sha256" -o "$temp/compose.sha256"
expected="$(awk 'NR == 1 {print $1}' "$temp/compose.sha256")"
[[ "$expected" =~ ^[a-f0-9]{64}$ && "$(sha256sum "$temp/docker-compose" | cut -d ' ' -f1)" == "$expected" ]] || { echo 'Compose checksum mismatch' >&2; exit 1; }
curl "${opts[@]}" "https://github.com/jqlang/jq/releases/download/$jq_version/jq-linux-amd64" -o "$temp/jq"
curl "${opts[@]}" "https://github.com/jqlang/jq/releases/download/$jq_version/sha256sum.txt" -o "$temp/jq.sha256"
expected="$(awk '$2 == "jq-linux-amd64" {print $1}' "$temp/jq.sha256")"
[[ "$expected" =~ ^[a-f0-9]{64}$ && "$(sha256sum "$temp/jq" | cut -d ' ' -f1)" == "$expected" ]] || { echo 'jq checksum mismatch' >&2; exit 1; }
mkdir -p "$HOME/.docker/cli-plugins" "$HOME/.local/bin"
install -m 700 "$temp/docker-compose" "$HOME/.docker/cli-plugins/docker-compose"
install -m 700 "$temp/jq" "$HOME/.local/bin/jq"
# SHA256 independently checked against the official v0.8.3 binary.
curl "${opts[@]}" https://github.com/regclient/regclient/releases/download/v0.8.3/regctl-linux-amd64 -o "$temp/regctl"
expected=b4f352d3acbe5cd3d2255ebac618a0f8da80b72f16ec30182275cb891f5e6ee5
[[ "$(sha256sum "$temp/regctl" | cut -d ' ' -f1)" == "$expected" ]] || { echo 'regctl checksum mismatch' >&2; exit 1; }
install -m 700 "$temp/regctl" "$HOME/.local/bin/regctl"
docker compose version
"$HOME/.local/bin/jq" --version
