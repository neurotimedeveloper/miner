#!/usr/bin/env bash
# Watch a systemd scope's cgroup and print its memory.peak until it disappears.
#
#   scripts/cgroup-peak.sh miner-1757000000
#
# cgroup v2 keeps memory.peak for the life of the cgroup; the scope is removed
# when its process exits, so this has to be running alongside the job.
set -u
UNIT="${1:?scope unit name}"
DIR="/sys/fs/cgroup/system.slice/$UNIT.scope"
for _ in $(seq 1 60); do [ -d "$DIR" ] && break; sleep 1; done
peak=0
while [ -d "$DIR" ]; do
  p=$(cat "$DIR/memory.peak" 2>/dev/null || echo 0)
  [ "$p" -gt "$peak" ] && peak=$p
  sleep 2
done
echo "cgroup memory.peak: $peak bytes ($(( peak / 1024 / 1024 )) MB)"
