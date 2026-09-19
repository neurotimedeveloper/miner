#!/usr/bin/env python3
"""Score a clusters.json against the reference airing list.

    scripts/evaluate.py var/month3.json real_fm_2026-08/ [--tol 3] [--days d1,d2] [--per-file] [--ad NAME] [--report DIR]

--report DIR writes the whole picture to DIR: summary.txt (the numbers),
per_file.csv (one line per reference file: airings, found, short, missing,
share), airings.csv (every listed airing with its verdict and our closest
cluster), and missed.txt (the not-found and short airings, grouped by ad,
readable).

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
    if "--report" in sys.argv:
        out_dir = sys.argv[sys.argv.index("--report") + 1]
        os.makedirs(out_dir, exist_ok=True)
        verdicts = []  # (file, ad, record_path, start_s, end_s, verdict, cluster, our_start, our_end)
        for name, start, dur, x in listed:
            end = start + dur
            i = bisect.bisect_right(starts, end)
            best, hit = 0, None
            for k in range(max(0, i - 200), i):
                a_start, a_end, cid, is_spot = airings[k]
                ov = min(a_end, end) - max(a_start, start)
                if ov > best:
                    best, hit = ov, k
            if best >= dur - tol:
                v = "found"
            elif best >= 0.5 * dur:
                v = "short"
            elif best > 1:
                v = "fragment"
            else:
                v = "missing"
            fs = file_start(x["record_path"])
            ours = ("", "", "")
            if hit is not None:
                a_start, a_end, cid, _ = airings[hit]
                ours = (cid, round(a_start - fs, 1), round(a_end - fs, 1))
            verdicts.append((x["_file"], name, x["record_path"], x["start_time"], x["end_time"], v) + ours)
        by_file = defaultdict(lambda: defaultdict(int))
        names = {}
        for f, name, *_rest in verdicts:
            by_file[f][_rest[3]] += 1
            names[f] = name
        def fnum(f):
            b = f.split(".")[0]
            return int(b) if b.isdigit() else 0
        with open(os.path.join(out_dir, "per_file.csv"), "w", newline="") as fh:
            w = csv.writer(fh)
            w.writerow(["file", "ad", "airings", "found", "short", "fragment", "missing", "share_found"])
            for f in sorted(by_file, key=fnum):
                c = by_file[f]
                tot = sum(c.values())
                w.writerow([f, names[f], tot, c["found"], c["short"], c["fragment"], c["missing"], f"{100*c['found']/tot:.1f}"])
        with open(os.path.join(out_dir, "airings.csv"), "w", newline="") as fh:
            w = csv.writer(fh)
            w.writerow(["file", "ad", "record_path", "start", "end", "verdict", "our_cluster", "our_start", "our_end"])
            for row in verdicts:
                w.writerow(row)
        with open(os.path.join(out_dir, "missed.txt"), "w") as fh:
            for f in sorted(by_file, key=fnum):
                bad = [r for r in verdicts if r[0] == f and r[5] != "found"]
                if not bad:
                    continue
                c = by_file[f]
                tot = sum(c.values())
                fh.write(f"== {f}  {names[f]}  ({c['found']} of {tot} found)\n")
                for r in bad:
                    ours = f"our cluster #{r[6]} at {r[7]}-{r[8]}s" if r[6] != "" else "nothing of ours there"
                    fh.write(f"   {r[5]:8s} {os.path.basename(r[2])} {r[3]}-{r[4]}s  {ours}\n")
                fh.write("\n")
        with open(os.path.join(out_dir, "summary.txt"), "w") as fh:
            tot = len(verdicts)
            cnt = defaultdict(int)
            for r in verdicts:
                cnt[r[5]] += 1
            fh.write(f"reference airings {tot} in {len(by_file)} files; our clusters {len(by_id)}\n")
            for v in ("found", "short", "fragment", "missing"):
                fh.write(f"{v:9s} {cnt[v]:5d}  {100*cnt[v]/tot:5.1f}%\n")
            levels = [100, 95, 90, 80, 50]
            for lvl in levels:
                k = sum(1 for f in by_file if 100 * by_file[f]["found"] / sum(by_file[f].values()) >= lvl)
                fh.write(f"files at >= {lvl:3d}% found: {k} of {len(by_file)}\n")
        print(f"report written to {out_dir}/: summary.txt, per_file.csv, airings.csv, missed.txt")
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
