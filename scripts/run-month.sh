#!/usr/bin/env bash
# Run miner over a month of recordings under a cgroup memory ceiling and report
# the peak the kernel saw - which is how acceptance criterion 4 is measured.
#
#   scripts/run-month.sh /data/MediaforCheck var/month [10G]
#
# Needs Linux with cgroup v2 and systemd (Ubuntu 22.04+). Run as root, or as a
# user allowed to use systemd-run --scope.
set -euo pipefail
cd "$(dirname "$0")/.."

IN="${1:?input directory}"
OUT="${2:-var/month}"
LIMIT="${3:-10G}"
UNIT="miner-$(date +%s)"

mkdir -p "$OUT" var/tmp
go build -o bin/miner ./cmd/miner

echo "running under MemoryMax=$LIMIT as $UNIT.scope; log: $OUT/run.log"
systemd-run --scope --unit="$UNIT" -p MemoryMax="$LIMIT" -p MemorySwapMax=0 \
  bin/miner -in "$IN" -out "$OUT" -temp var/tmp -tz UTC \
  -max-concurrent "$(( $(nproc) - 2 ))" -no-representatives \
  > "$OUT/run.log" 2>&1 || echo "miner exited with status $? (see $OUT/run.log)"

# The scope is gone once the process exits, so the peak is read from the log
# the kernel leaves behind: memory.peak is captured by a watcher while it runs.
echo "peak RSS as the run reported it:"
grep -E "peak rss|took|clusters" "$OUT/run.log" || true
echo
echo "summary:"
python3 scripts/summarise.py "$OUT/clusters.json" | head -30
