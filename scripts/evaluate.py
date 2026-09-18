#!/usr/bin/env python3
"""Score a clusters.json against the reference airing list.

    scripts/evaluate.py var/month3.json real_fm_2026-08/ [--tol 3] [--days 2026-08-01,2026-08-02] [-v]

The reference is a directory of TSV files, one per ad, as delivered by
production: columns id, result_id, name, dur, record_path, start_time,
end_time — start/end in whole seconds from the beginning of the recording
file named in record_path, whose name begins with its broadcast timestamp.

Reported, as the acceptance criteria ask:
  recall        share of listed airings that fall inside one of our airings
  fragmentation clusters per listed ad (target 1)
  flag          share of the covering clusters that are flagged spot
A cluster that covers nothing listed is not an error: the list holds only the
catalogue ads production found.
"""
import bisect, csv, glob, os, re, sys
from collections import defaultdict
from datetime import datetime, timezone

NAME_RE = re.compile(r"^(\d{4}-\d{2}-\d{2})-(\d{2})-(\d{2})-(\d{2})")


def file_start(path):
    m = NAME_RE.match(os.path.basename(path))
    if not m:
        sys.exit(f"no timestamp in {path}")
    d, h, mi, s = m.groups()
    return datetime.fromisoformat(f"{d}T{h}:{mi}:{s}").replace(tzinfo=timezone.utc).timestamp()


def parse_time(s):
    s = s.strip()
    if s.endswith("Z"):
        s = s[:-1]
    t = datetime.fromisoformat(s)
    if t.tzinfo is None:
        t = t.replace(tzinfo=timezone.utc)
    return t.timestamp()


def load_reference(d):
    rows = []
    for f in sorted(glob.glob(os.path.join(d, "*.tsv")), key=lambda f: int(os.path.basename(f).split(".")[0]) if os.path.basename(f).split(".")[0].isdigit() else 0):
        with open(f, newline="") as fh:
            for x in csv.DictReader(fh, delimiter="\t"):
                x["_file"] = os.path.basename(f)
                start = file_start(x["record_path"]) + float(x["start_time"])
                rows.append((x["name"], start, float(x["dur"]), x))
    return rows


def main():
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    tol = float(sys.argv[sys.argv.index("--tol") + 1]) if "--tol" in sys.argv else 3.0
    verbose = "-v" in sys.argv
    import json
    res = json.load(open(sys.argv[1]))
    airings = []  # (start, end, cluster id, is spot)
    by_id = {c["ID"]: c for c in res["Clusters"]}
    for c in res["Clusters"]:
        for o in c["Occurrences"]:
            airings.append((parse_time(o["Start"]), parse_time(o["End"]), c["ID"], c["IsSpot"]))
    airings.sort()
    starts = [a[0] for a in airings]

    listed = load_reference(sys.argv[2])
    if "--days" in sys.argv:
        days = set(sys.argv[sys.argv.index("--days") + 1].split(","))
        listed = [r for r in listed if os.path.basename(r[3]["record_path"])[:10] in days]
    covered = 0
    per_ad = defaultdict(lambda: defaultdict(int))  # ad -> cluster -> airings
    missed = defaultdict(int)
    total = defaultdict(int)
    flagged = 0
    partial = 0
    for name, start, dur, _ in listed:
        total[name] += 1
        end = start + dur
        i = bisect.bisect_right(starts, end)
        hit = None
        best = 0
        best_fit = 0
        for k in range(max(0, i - 200), i):
            a_start, a_end, cid, is_spot = airings[k]
            ov = min(a_end, end) - max(a_start, start)
            # The best covering airing: most of the listed airing, then the
            # tightest fit, so a unit that also spans the neighbour does not
            # take airings from the ad's own cluster.
            fit = ov / max(a_end - a_start, 1)
            if ov > best + 0.5 or (abs(ov - best) <= 0.5 and fit > best_fit):
                best, best_fit, hit = ov, fit, k
        # Covered: our airing overlaps most of the listed one. The list's
        # times are whole seconds, hence the tolerance.
        if hit is None or best < dur - tol:
            if hit is not None and best > 0.5 * dur:
                partial += 1
            missed[name] += 1
            continue
        covered += 1
        per_ad[name][airings[hit][2]] += 1
        if airings[hit][3]:
            flagged += 1

    n = len(listed)
    ads = sorted(total, key=lambda a: -total[a])
    print(f"reference: {n} airings of {len(ads)} ads; our clusters: {len(by_id)}")
    print(f"recall: {covered}/{n} airings covered = {100*covered/n:.2f}%  "
          f"({partial} more overlap by half or more but not within {tol:.0f}s)")
    print(f"flagged spot among covered: {flagged}/{covered} = {100*flagged/max(covered,1):.1f}%")
    frag = {a: len(per_ad[a]) for a in ads if a in per_ad}
    ones = sum(1 for v in frag.values() if v == 1)
    print(f"fragmentation: {ones} of {len(frag)} covered ads in exactly one cluster; "
          f"mean {sum(frag.values())/len(frag):.2f} clusters per ad")
    # Fragmentation the acceptance way: the main cluster holds how much?
    main_share = []
    for a in frag:
        cs = per_ad[a]
        main_share.append(max(cs.values()) / sum(cs.values()))
    print(f"share of an ad's covered airings in its largest cluster: mean {100*sum(main_share)/len(main_share):.1f}%")
    print()
    print(f"{'airings':>7} {'found':>6} {'clusters':>8} {'dur':>6}  ad")
    for a in ads:
        k = total[a]
        found = k - missed[a]
        cs = per_ad[a]
        cl = ""
        if cs:
            cl = " ".join(f"#{c}x{m}" for c, m in sorted(cs.items(), key=lambda kv: -kv[1])[:4])
            if len(cs) > 4:
                cl += f" +{len(cs)-4}"
        dur = next(d for nm, _, d, _ in listed if nm == a)
        flag = ""
        if cs:
            top = max(cs, key=cs.get)
            flag = "spot" if by_id[top]["IsSpot"] else "NOT SPOT: " + by_id[top]["Reason"][:50]
        print(f"{k:7d} {found:6d} {len(cs):8d} {dur:6.1f}  {a[:45]:45s} {cl}  {flag}")
    if "--per-file" in sys.argv:
        # One line per reference file, the way the list was delivered:
        # file, ad, airings, found, share found. Then how many ads are
        # complete at each level.
        by_file = {}
        for name, start, dur, x in listed:
            f = x["_file"]
            by_file.setdefault(f, [name, 0])
            by_file[f][1] += 1
        found_by_file = defaultdict(int)
        for name, start, dur, x in listed:
            pass
        # Recompute coverage per row (same rule as above).
        for name, start, dur, x in listed:
            end = start + dur
            i = bisect.bisect_right(starts, end)
            best = 0
            for k in range(max(0, i - 200), i):
                a_start, a_end, cid, is_spot = airings[k]
                ov = min(a_end, end) - max(a_start, start)
                if ov > best:
                    best = ov
            if best >= dur - tol:
                found_by_file[x["_file"]] += 1
        print()
        print(f"{'file':>8} {'airings':>7} {'found':>6} {'share':>6}  ad")
        levels = {100: 0, 95: 0, 90: 0, 80: 0, 50: 0}
        for f, (name, n) in sorted(by_file.items(), key=lambda kv: int(kv[0].split(".")[0]) if kv[0].split(".")[0].isdigit() else 0):
            k = found_by_file[f]
            share = 100 * k / n
            for lvl in levels:
                if share >= lvl:
                    levels[lvl] += 1
            print(f"{f:>8} {n:7d} {k:6d} {share:5.1f}%  {name[:50]}")
        print()
        for lvl, k in levels.items():
            print(f"ads at >= {lvl:3d}% found: {k} of {len(by_file)}")
    if "--ad" in sys.argv:
        # What our output looks like at this ad's airings: every cluster
        # whose airings overlap them, by how much, at what offset.
        want = sys.argv[sys.argv.index("--ad") + 1].lower()
        from collections import Counter
        prof = Counter()
        for name, start, dur, _ in listed:
            if want not in name.lower():
                continue
            end = start + dur
            i = bisect.bisect_right(starts, end)
            for k in range(max(0, i - 200), i):
                a_start, a_end, cid, is_spot = airings[k]
                ov = min(a_end, end) - max(a_start, start)
                if ov > 1:
                    prof[(cid, round(by_id[cid]["DurationSec"]), round(a_start - start), by_id[cid]["IsSpot"])] += 1
        print(f"\nclusters overlapping airings of '{want}' (cluster, its length, offset from the listed start, spot?) -> airings:")
        for (cid, d, off, sp), n in prof.most_common(12):
            print(f"  #{cid:5d} {d:4d}s at {off:+4d}s {'spot' if sp else '-':4s} x{n}  {by_id[cid]['Reason'][:70]}")


if __name__ == "__main__":
    main()
