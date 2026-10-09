#!/usr/bin/env bash
# Administrator-only, explicit maintenance operation. Retains the old store.
set -euo pipefail
export PATH=/usr/sbin:/sbin:/usr/bin:/bin
mount_dir="${1:?usage: migrate-docker-storage.sh MOUNT_DIR [--migrate]}"
[[ "$mount_dir" =~ ^/[a-zA-Z0-9_./-]+$ ]] || exit 1
[[ "$(id -u)" == 0 ]] || { echo 'Run as root' >&2; exit 1; }
systemctl is-active --quiet docker.service || { echo 'Docker must be managed by the active docker.service; review manually started daemons before migration' >&2; exit 1; }
mountpoint -q "$mount_dir" || { echo 'Destination filesystem is not mounted' >&2; exit 1; }
[[ "$(findmnt -n -o FSTYPE --target "$mount_dir")" == xfs ]] || { echo 'Expected an existing XFS filesystem; no formatting is performed' >&2; exit 1; }
xfs_info "$mount_dir" | grep -q 'ftype=1' || { echo 'Docker overlay2 requires XFS ftype=1' >&2; exit 1; }
old="$(docker info --format '{{.DockerRootDir}}')"
new="$mount_dir/docker-data"
[[ -d "$old" && "$old" != "$new" && ! -e "$new" ]] || { echo 'Old store unavailable or destination already exists' >&2; exit 1; }
[[ -f /etc/docker/daemon.json ]] || { echo 'Expected existing daemon.json' >&2; exit 1; }
systemctl show docker -p ExecStart | grep -q -- '--data-root' && { echo 'Remove the conflicting systemd --data-root argument before migration' >&2; exit 1; }
source_kib="$(du -sxk "$old" | awk '{print $1}')"
available_kib="$(df -Pk "$mount_dir" | awk 'NR == 2 {print $4}')"
[[ "$available_kib" -gt $((source_kib + 20971520)) ]] || { echo 'Insufficient destination capacity' >&2; exit 1; }
echo "Copy Docker store: $old -> $new ($source_kib KiB). Original is retained."
docker ps --format 'Will stop/restart: {{.Names}}'
[[ "${2:-}" == --migrate ]] || { echo 'Inspection only. Add --migrate only during an approved maintenance window.'; exit 0; }
[[ ! -e /etc/systemd/system/docker.service.d/weknora-storage.conf ]] || { echo 'Existing storage drop-in; review it manually' >&2; exit 1; }

umask 077
mount -o remount,rw "$mount_dir"
state="$mount_dir/.weknora-docker-migration-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir "$state"
cp -a /etc/docker/daemon.json "$state/daemon.json.before"
cp -a /etc/fstab "$state/fstab.before"
printf '%s\n' "$old" > "$state/old-data-root"
trap 'echo "Migration stopped on an error. Original data remains at $old; backups and running container IDs are in $state. Review before recovery." >&2' ERR

python - "$new" "$state/daemon.json.after" <<'PY'
import json, sys
with open('/etc/docker/daemon.json') as source:
    config = json.load(source)
config['data-root'] = sys.argv[1]
with open(sys.argv[2], 'w') as target:
    json.dump(config, target, indent=2)
    target.write('\n')
PY
dockerd --validate --config-file "$state/daemon.json.after"

mkdir "$new"
# Warm copy while services run; the second copy after stopping is authoritative.
copy_status=0
rsync -aHAXx --numeric-ids --stats "$old/" "$new/" || copy_status=$?
[[ "$copy_status" == 0 || "$copy_status" == 24 ]] || exit "$copy_status"

docker ps -q > "$state/running-containers"
mapfile -t running < "$state/running-containers"
if [[ "${#running[@]}" -gt 0 ]]; then
  docker stop "${running[@]}"
fi
if systemctl list-unit-files docker.socket --no-legend | grep -q docker.socket; then
  systemctl stop docker.socket
fi
systemctl stop docker.service
[[ -z "$(pgrep -x dockerd || true)" ]] || { echo 'A Docker daemon is still running; aborting before final synchronization' >&2; exit 1; }
# Destination was freshly created above, never an existing business directory.
rsync -aHAXx --numeric-ids --delete --stats "$old/" "$new/"

device="$(findmnt -n -o SOURCE --target "$mount_dir")"
uuid="$(blkid -s UUID -o value "$device")"
if ! awk -v target="$mount_dir" '$1 !~ /^#/ && $2 == target {found=1} END {exit !found}' /etc/fstab; then
  printf 'UUID=%s %s xfs defaults 0 0\n' "$uuid" "$mount_dir" >> /etc/fstab
fi
mkdir -p /etc/systemd/system/docker.service.d
printf '[Unit]\nRequiresMountsFor=%s\n' "$mount_dir" > /etc/systemd/system/docker.service.d/weknora-storage.conf
cp "$state/daemon.json.after" /etc/docker/daemon.json
systemctl daemon-reload
systemctl start docker.service
[[ "$(docker info --format '{{.DockerRootDir}}')" == "$new" ]]
if [[ "${#running[@]}" -gt 0 ]]; then
  docker start "${running[@]}"
fi
docker ps --format '{{.Names}} {{.Status}}'
echo "Storage switched successfully. Do not delete $old until recovery and all existing services have been verified."
echo "Configuration backups: $state"
