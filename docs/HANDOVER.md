# HANDOVER

Where `miner` stands, what the real month of recordings revealed, and what to
do next. Last updated 2026-09-10, end of the first day on real data.

---

## Status in one paragraph

The method works: on synthetic broadcasts with known truth it finds every
airing, one cluster per repeat, every spot flag right (19 tests, `-race` clean).
On real material it runs and produces plausible results — but the **full-month
run has not yet completed**. Three attempts were made today; the first two were
stopped for defects they exposed (both fixed), the third was still in its
feature phase when the day ended. There is no acceptance number for the month
yet, and there is no spot list to measure recall against.

## The data

`MediaforCheck/` — 31 days × 24 hour-files of `real_fm`, srv 6, 1–31 August
2026, mp3 22050 Hz stereo 64 kbps, 61 minutes each (consecutive files overlap
by a minute), 20 GB. **No spot list was supplied with it**; acceptance
criterion 1 (recall against the list) cannot be measured until it arrives.

## What one real day showed, and what was fixed

`bin/miner -in MediaforCheck/2026-08-01` — 24 hours, 62 s, 281 MB peak.

| finding | before | fix | after |
|---|---|---|---|
| **Choruses.** 448 of 557 "spots" were two stretches less than five minutes apart: music repeating inside its own track. | 1299 clusters, 557 spots | matches at lags under `MinLagSec` (5 min) are never made | 265 / 117 |
| **Song verses as spots.** A song played twice is one long match; its chorus also matches across the plays at lag ± a chorus period, cutting both plays into verse-sized "spots" of two airings an hour apart. | | a match inside a match half again as long, at a different lag, is that repeat's internal periodicity and is dropped | 216 / 78; eleven songs came out whole at 140–209 s |
| **Airing positions** were exact only to the boundary tolerance (±0.5 s). | audit flagged 28/216 clusters at ±3 frames | every airing re-placed against the cluster's reference airing | 215/216 clusters consistent to a frame |
| **61-minute files** kept the overlapping minute twice — a repeat of itself every hour. | | the previous file's overlap is dropped at the join | |

After these, the day's 216 clusters are internally consistent: `TestAudit`
re-scored every airing against its cluster's first airing and 215 of 216
clusters scored every airing above 0.5. Spot durations 8–47 s (two at 72/85 s);
non-spots are 138 idents/stingers under 8 s and 11 songs.

## What the month attempts showed

**Attempt 1** (4096-point transform per frame): still decoding after 55 min.
Feature extraction was 80× more expensive than it needed to be. Fixed: ten
64 ms power spectra are averaged per stored frame (Welch), same 512 ms
integration, same separation on real audio (same-audio 5th pct 0.58 vs 0.59,
different 95th pct 0.25 vs 0.23), fraction of the cost.

**Attempt 2** (Welch features): features 25 min, search 30 min, then **killed
by the OS in clustering — peak memory footprint 31.7 GB on an 8 GB machine.**
Cause: matching every airing against every earlier one is quadratic. A spot
airing 150 times a month is 11 000 matches; an ident airing sixty times a day is
1.7 million; and the boundary voting held a map per cut. Fixed twice over:
each frame may now be explained by at most six distinct matches (union-find
needs one, boundary evidence needs two, a shared stinger needs a few more), the
same pair is never re-verified from a neighbouring window, and votes are flat
arrays counted in distinct match ids. The search loop was also parallelised in
hour chunks. All synthetic tests still pass.

**Attempt 3** (bounded matching, single-threaded search binary): started
18:00 and **was stopped at 18:15**, in its feature phase, when the session
ended. Nothing came out of it. The month run has to be started again — the
current binary has the parallel search and should take roughly 25 min for
features plus about 10 min for search on this machine; watch
`var/month/run.log`, and read the result with
`python3 scripts/summarise.py var/month/clusters.json`.

**This machine has 8 GB of RAM.** It cannot validate the 10 GB criterion; that
needs Linux with cgroups. The estimate for a month is ~3 GB of features and
index plus the matches; attempt 2's 2.1 GB maximum RSS before the clustering
blow-up is consistent with it.

## Exact commands

```sh
make build
# one day, ~1 min
bin/miner -in MediaforCheck/2026-08-01 -out var/day -temp var/tmp -tz UTC -no-representatives
# the month, run detached; watch var/month/run.log
/usr/bin/time -l bin/miner -in MediaforCheck -out var/month -temp var/tmp -tz UTC -no-representatives > var/month/run.log 2>&1 &
# what came out
python3 scripts/summarise.py var/month/clusters.json
# consistency audit of a result against its audio (a day: ~1 min)
AUDIT=var/day/clusters.json AUDIT_IN=MediaforCheck/2026-08-01 go test -run TestAudit -v
# re-check features/thresholds on two captures of one broadcast
MINER_CAL_A=a.mp3 MINER_CAL_B=b.mp3 MINER_CAL_LAG=4.35 go test -run TestCalibrate -v
```

Representatives (`-no-representatives` omitted) write one FLAC per cluster to
`var/<out>/representatives/` — the way to *listen* to what was found.

## What to do next, in order

1. **Run the month** (command below, ~35 min, nothing else heavy alongside it)
   and read `summarise.py`'s output: total clusters,
   spot count, airings per day, the "fragmentation candidates" list (same
   length, disjoint in time — the same spot found twice), peak RSS, wall time.
2. **Get the spot list from the dev**, in whatever format it exists, plus the
   existing tool's output on the same month and its evaluation script. Without
   the list the 92 % cannot be compared to anything. Then write the evaluator
   (recall per airing, clusters per spot, precision on a sample).
3. **Listen.** Extract representatives for the day, sort spots by airing count,
   and have someone who knows the station listen to twenty of them. Ten minutes
   of listening will tell more about the spot/non-spot rules than any amount of
   reasoning.
4. **Tune the classifier on the list**: the 8 s / 120 s bounds, the hourly rule
   (it does not catch elements scheduled at *two* minutes of the hour, e.g. a
   43 s element at :22 and :52 every hour on the real day), and whether idents
   under 8 s should be reported at all.
5. **Memory measurement on Linux** with cgroups, for the record.

## Open questions

- What does the dev's 92 % measure — recall of airings, of spots, precision?
- Which 8 % does it miss? Those spots are where this method has to win.
- Is `real_fm` a music station? The day looked like one (choruses, songs
  twice a day); the classifier's assumptions are tuned to that.
- Is a 10 s element that airs twice back-to-back every hour (found: x32 on the
  day) a spot or station imaging?
