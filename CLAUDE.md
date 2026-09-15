# CLAUDE.md

Working notes for this repository. Read `README.md` for usage and
`docs/DESIGN.md` for the decisions and their costs.

## Where things stand

Read `docs/HANDOVER.md` first. Short form: the method is proven on synthetic
truth and runs on real data; the first full-month run is not yet complete; no
spot list has been supplied, so recall against the list is unmeasured.

## What this is

A Go package (`miner`) that finds repeating segments in a broadcast recording,
clusters their airings, and flags which clusters are advertising spots. It is a
sibling of `merger` (same author, same structure, same conventions) but a
separate project: no shared module, no import in either direction. `internal/ff`
is a trimmed copy of merger's, on purpose.

## The one idea

> **A repeat is confirmed and bounded by correlation, not by a hash.**

The index only proposes candidate pairs. Every pair is then walked frame by
frame along its lag, and grown until the audio stops agreeing. Boundaries,
"same or merely similar", and every acceptance number come from that walk.
Anything that shortcuts it - trusting the index, trusting a single match's
boundary - is where recall and fragmentation are lost.

## Commands

```sh
make build test test-short test-race fmt vet
```

Requires Go 1.27+ and ffmpeg on PATH.

## Architecture

```
miner.go        Mine: decode -> features -> index -> verify -> cluster -> classify -> representatives
features.go     log-mel frames at 20 fps, 512 ms analysis frame; gain and running-mean removed
index.go        window descriptors (2-D DCT) and the seeded LSH index; descriptorStream for per-frame queries
verify.go       lag refinement, growth with hysteresis, the sliding half-second score
cluster.go      boundary votes, atomic segments, union-find, always-adjacent joining
classify.go     spot / non-spot with a reason per cluster
timeline.go     inputs on one frame axis, with discontinuities recorded
synth_test.go   the synthetic broadcast with known truth, and the evaluator
calibrate_test.go / audit_test.go   hand-run diagnostics on real audio (env-gated)
scripts/summarise.py   what a clusters.json contains, and what looks wrong in it
cmd/miner/      CLI, JSON, report
internal/ff/    ffmpeg runner: timeouts, process groups, streamed decode, extraction
internal/pieces/ file names and manifests -> inputs
```

## Rules that must not be broken

| Rule | Why |
|---|---|
| Never trust the index alone | It says "looks alike"; only verification says "is the same". |
| Never query the index at the index's own step | The descriptor is phase-sensitive; two airings align to ±5 frames and one phase in ten lines up. Query at every frame. Recall 67 % → 100 %. |
| Never judge agreement on one frame | Same audio through two mp3 encodes disagrees on single frames; over half a second it does not. |
| Never shorten the analysis frame below 512 ms without re-measuring | Real cross-chain audio agreed on 17 % of an hour at 64 ms and 98 % at 512 ms. |
| Never drop CMN | Without it unrelated frames of one station agree 60 % of the time. |
| Never make one match's endpoint a cut | A single early ending inside a spot splits every airing of it. Support is counted in distinct matches and carried across matches. |
| Never treat a matched interval as an occurrence | Paired spots fragment into "paired" and "solo" clusters. Cut at boundaries first. |
| Never join an always-adjacent long repeat to its neighbour | A song that follows a spot hands the spot the song's verdict. |
| Never match two stretches closer than `MinLagSec` | A chorus repeats inside its song every minute; on a real day 448 of 557 "spots" were choruses. Airings hours apart link the rest. |
| Never keep a match that sits inside a longer match at another lag | It is the longer repeat's own periodicity; kept, it cuts every song into verse-sized "spots". |
| Never keep a 61-minute file's overlap with the next | It is a repeat of itself at every hour. |
| Never let a map iteration reach the output | Determinism is an acceptance criterion. |

## Gotchas found the hard way

- **The stored frame integrates 512 ms** - ten 512-point power spectra
  averaged (`SmoothFrames`), hopped every 400 samples. It replaced a single
  4096-point transform per hop, which gave the same separation (same p05 0.58
  vs 0.59, different p95 0.25 vs 0.23) at eighty times the cost. Changing the
  span changes every threshold; `TestCalibrate` is how they were set.
- **`TestCalibrate` and `TestAudit` are hand-run diagnostics**, gated by
  environment variables. Use them before and after any change to features or
  thresholds; the numbers in DESIGN.md come from them.
- **The synthetic voice must be varied.** An early generator with a narrow
  pitch range made unrelated stretches agree over 512 ms frames in a way real
  speech does not, and made the method look broken. `voice.render` has two
  formants, twelve harmonics, shaped-noise consonants and pauses for that
  reason.
- **`minLen` is `MinRepeatSec*FPS - FrameSize/Hop/2`.** A repeat of length L
  agrees over about L minus half a frame; the frames straddling its edges are
  half something else.
- **A 3.0 s repeat sits exactly on the floor** and is found in only some of its
  airings. The acceptance fixture's shortest ident is 3.5 s for that reason.
- `ls -R` may hang in this shell; use `find`.
- **This machine has 8 GB of RAM.** A month's run that grows past a few GB gets
  compressed, then killed. `/usr/bin/time -l` reports "peak memory footprint"
  including compressed pages — attempt 2 showed 31.7 GB there. Do not run two
  month jobs at once, and do not run the test suite alongside one.
- **Long runs need visible progress.** `Options.Progress` reports features,
  index and search; the CLI prints them to stderr. Watch `run.log`; never
  assume a silent hour is a hung run or a healthy one.
- **Matching is bounded per frame (`maxMatchesPerFrame`).** All-pairs matching
  is quadratic in airings per repeat and ran the machine out of memory. Six
  is enough for union-find, boundary votes and a shared stinger.
