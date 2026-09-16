# HANDOVER

Where `miner` stands, what the real month of recordings revealed, and what to
do next. Last updated 2026-09-10, end of the first day on real data.

---

## Status in one paragraph

The method works: on synthetic broadcasts with known truth it finds every
airing, one cluster per repeat, every spot flag right (21 tests, `-race` clean).
The real month runs on the server in 24 min at 5.2 GB peak RSS (ceiling 10).
The output's fragmentation was measured against its own audio — 287 of 6 813
spot clusters were wholly the same audio as another — and the two mechanisms
behind it were found and fixed on 2026-09-16 (below); the month has not yet
been re-run with those fixes. There is still no spot list, so recall against
the list is unmeasured.

## 2026-09-16: fragmentation measured and two causes fixed

**How it was measured without a list.** `TestFragmentation` cuts each spot
cluster's reference airing out of the audio, lays the 6 813 excerpts on a
timeline with a break between each, and mines them. Representatives that
repeat each other are the same audio in two clusters. Result on the month:
1 415 groups share *something* (mostly a 3–5 s tag shared by many creatives of
one advertiser — those are correctly separate); **287 clusters were wholly
the same audio as another cluster, in 227 groups.**

**Cause 1 — a dropout in one airing cut every airing.** A 251 s song, aired
four times; on 2026-08-05 the stream lost 1.3 s in the middle. That airing's
three matches against the others all broke there, and three "independent"
votes cut the song into 104 s + 145 s for every airing — and 104 s is
spot-length. Fixed in `bridgeDropouts` (`miner.go`): a match that continues
within 5 s on both sides at a lag shifted by no more than the hole is one
match, carrying both lags (`match.Split/Lag2`). Test:
`TestADropoutInOneAiringDoesNotCutTheOthers`.

**Cause 2 — soft edges and starved matches.** An element whose end lands
anywhere within 1.5 s came out as twelve 8–10 s clusters: segments whose ends
differ by more than 0.5 s never unite. Fixed by a second pass, `merge.go`:
cluster references are mined against each other (the same machinery as the
audit) and clusters whose references agree over 80 % of the *longer* are
joined. Measured against the *shorter* it went badly wrong — a stinger
absorbed every spot it sat inside — hence the rule.

**Also:** a song cut in pieces was classified as spots. Clusters that follow
each other in 80 % of airings both ways are pieces of one unit; if the unit is
over 120 s none of them is a spot. And `dedupeMatches` merged across breaks
(two excerpts side by side became one 34 s match); fixed.

**On 4 days (2026-08-01/02/04/05), Mac:** 1 089 clusters / 462 spots →
**933 / 399**, no airing lost (6 423 both), runs identical twice.
`-dump-matches FILE` writes the raw matches; `MINER_DEBUG_MERGE=1` logs joins.

**Next:** rsync to the server, re-run the month, re-run `TestFragmentation`
on the new clusters.json (the excerpt cache must be deleted first — it is
keyed to the old cluster list), and read `summarise.py` again.


## The data

`MediaforCheck/` — 31 days × 24 hour-files of `real_fm`, srv 6, 1–31 August
2026, mp3 22050 Hz stereo 64 kbps, 61 minutes each (consecutive files overlap
by a minute), 20 GB. **No spot list was supplied with it**; acceptance
criterion 1 (recall against the list) cannot be measured until it arrives.

## 2026-09-15: the month ran on the server

Server: Ubuntu 24 container (no systemd, cgroup read-only), 20 cores, 62 GB.
Code and data went over by rsync (`/root/miner`, `/data/MediaforCheck`, 742
files after the two srv10 duplicates were moved to `/data/extra`).

`/usr/bin/time -v bin/miner -in /data/MediaforCheck -out var/month -temp var/tmp -tz UTC -max-concurrent 16 -no-representatives`

| | result |
|---|---|
| wall | **22.3 min** (features + search + clustering, 358 % CPU) |
| maximum RSS | **9.72 GB** — under the ceiling, far too close to it |
| clusters | 13 789: 6 813 spots, 6 976 other |
| spot airings | 40 258, i.e. 1 302 per day — roughly 3× what a station airs |
| matches | 226 738 from 2.0 M verified of 203 M seed pairs |
| skipped / exit | 0 / 0 |

**What is right.** The heavy rotation is found and held together: a 21.8 s
spot 348 times over 31 days (11/day), a 10.8 s spot 395 times over 26 days,
a 33.6 s spot 241 times over 22 days. Those are real advertisements in one
cluster each across the whole month.

**What is wrong, in order of priority.**

1. **Too many clusters, most of them small.** 2 296 "spots" air only 2–3
   times; 593 "spots" are 60–120 s long (song-shaped); 99 636 pairs of clusters
   have the same length and disjoint dates (the same repeat found twice, or
   music). The classifier and the fragmentation both need the real list — or
   twenty minutes of listening to representatives sorted by airing count.
   Suspects, in order: (a) music that the internal-repeat filter does not
   catch when a song airs only twice a month with a lag over `MaxRepeatSec`;
   (b) boundary votes across 30 days of airings splitting long-lived spots
   when their edges drift; (c) `maxMatchesPerFrame = 6` starving boundary
   evidence for repeats with hundreds of airings.
2. ~~Memory is 3× the estimate.~~ **Fixed 2026-09-16**: a soft limit
   (`debug.SetMemoryLimit`, `-memory-limit`, default 6 GB), batched decode
   into a pre-reserved timeline, int8 descriptors hashed as they are made,
   chunk-sized coverage per search worker. Same command, same data:
   **24.2 min, maximum RSS 5.20 GB** (`time -v`: 5 200 948 kB), 13 800
   clusters (6 813 spots — identical), exit 0. Live data is about 2.7 GB;
   the rest is the collector's slack and shrinks with a lower `-memory-limit`.
   Two months fit under the ceiling with room.
3. **cgroup measurement is not possible inside this container** (read-only
   cgroup fs). The container's own `memory.peak` read 31 GB, which includes
   page cache for 20 GB of mp3 and is not the process. Ask for a run on the
   host, or a container with cgroup delegation, for the record.
4. **No spot list yet.** Criterion 1 remains unmeasured.

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

0. ~~Memory first~~ — done. ~~Fragmentation causes~~ — two found and fixed;
   re-run the month to measure.
1. **Read the month** with `summarise.py`: total clusters,
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
