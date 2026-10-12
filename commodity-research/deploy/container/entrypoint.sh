#!/usr/bin/env bash
set -Eeuo pipefail

# Authenticated Workbench initializes/migrates each workspace on first access.
# Do not create a legacy default researcher or global database here.
if [[ "$(id -u)" == 0 ]]; then
  mkdir -p "${TIN_DATA_DIR:-/data}" /run/commodity
  chown commodity:commodity "${TIN_DATA_DIR:-/data}" /run/commodity
  # Imported/restored workspace files can inherit the host UID. The service
  # needs to update its own databases/staging; do not touch the bound token.
  for directory in workspaces import_staging exports; do
    if [[ -d "${TIN_DATA_DIR:-/data}/$directory" ]]; then
      chown -R commodity:commodity "${TIN_DATA_DIR:-/data}/$directory"
    fi
  done
  if [[ -n "${TIN_SERVICE_TOKEN_FILE:-}" && -f "$TIN_SERVICE_TOKEN_FILE" ]]; then
    install -o commodity -g commodity -m 600 "$TIN_SERVICE_TOKEN_FILE" /run/commodity/research-token
    export TIN_SERVICE_TOKEN_FILE=/run/commodity/research-token
  fi
  exec gosu commodity "$@"
fi
exec "$@"
