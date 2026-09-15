#!/usr/bin/env python3
"""Summarise a clusters.json: what was found, how it is distributed, and the
shapes that suggest a problem (fragmentation candidates, oddities)."""
import json, sys, datetime as dt, collections, statistics

path = sys.argv[1] if len(sys.argv) > 1 else "var/month/clusters.json"
r = json.load(open(path))
cl = r["Clusters"]
def ts(s): return dt.datetime.fromisoformat(s.replace("Z", "+00:00"))

spots = [c for c in cl if c["IsSpot"]]
others = [c for c in cl if not c["IsSpot"]]
st = r["Stats"]
print(f"files {st['Files']}, audio {st['AudioSec']/3600:.1f} h, clusters {len(cl)} ({len(spots)} spots, {len(others)} other)")
print(f"peak RSS {st['PeakRSSBytes']/1e9:.2f} GB, features {st['FeatureBytes']/1e9:.2f} GB, index {st['IndexBytes']/1e9:.2f} GB, wall {st['WallSec']/60:.1f} min")
print(f"windows {st['Windows']}, seed pairs {st['SeedPairs']}, verified {st['VerifiedPairs']}, matches {st['Matches']}")

def hist(vals, edges):
    out = collections.OrderedDict()
    for lo, hi in zip(edges, edges[1:] + [1e9]):
        out[f"{lo}-{hi if hi < 1e9 else ''}"] = sum(1 for v in vals if lo <= v < hi)
    return " ".join(f"{k}:{v}" for k, v in out.items())

print("\nspot airings per cluster:", hist([len(c["Occurrences"]) for c in spots], [2, 3, 5, 10, 20, 50, 100, 200]))
print("spot durations (s):      ", hist([c["DurationSec"] for c in spots], [8, 10, 15, 20, 30, 45, 60, 90, 120]))
print("other airings per cluster:", hist([len(c["Occurrences"]) for c in others], [2, 3, 5, 10, 20, 50, 100, 200, 500]))
print("other durations (s):     ", hist([c["DurationSec"] for c in others], [0, 4, 8, 60, 120, 200, 300]))

tot = sum(len(c["Occurrences"]) for c in spots)
print(f"\nspot airings total {tot}; per day {tot/max(1,st['AudioSec']/86400):.0f}")

# Days covered by each spot cluster
print("\ntop spots by airings:")
for c in sorted(spots, key=lambda c: -len(c["Occurrences"]))[:15]:
    days = sorted({ts(o["Start"]).date() for o in c["Occurrences"]})
    per_day = len(c["Occurrences"]) / len(days)
    print(f"  #{c['ID']:5d} {c['DurationSec']:5.1f}s x{len(c['Occurrences']):4d} over {len(days):2d} days ({per_day:.1f}/day) {days[0]}..{days[-1]}")

# Fragmentation candidates: spot clusters of near-equal duration whose date
# ranges do not overlap much (the same spot found as two clusters).
print("\nfragmentation candidates (same length, disjoint in time):")
byd = sorted(spots, key=lambda c: c["DurationSec"])
n = 0
for i, a in enumerate(byd):
    for b in byd[i+1:]:
        if b["DurationSec"] - a["DurationSec"] > 0.4: break
        ta = [ts(o["Start"]) for o in a["Occurrences"]]; tb = [ts(o["Start"]) for o in b["Occurrences"]]
        if max(ta) < min(tb) or max(tb) < min(ta):
            n += 1
            if n <= 10:
                print(f"  #{a['ID']} {a['DurationSec']:.1f}s x{len(ta)} {min(ta):%m-%d}..{max(ta):%m-%d}  |  #{b['ID']} {b['DurationSec']:.1f}s x{len(tb)} {min(tb):%m-%d}..{max(tb):%m-%d}")
print(f"  {n} candidate pairs")

# Reasons
print("\nverdict reasons:")
rs = collections.Counter()
import re
for c in cl:
    rs[re.sub(r"[0-9.]+s?", "N", c["Reason"])[:70]] += 1
for k, v in rs.most_common(8): print(f"  {v:5d}  {k}")
