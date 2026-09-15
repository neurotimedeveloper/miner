# Spec: Finding Repeats in Broadcast Audio (`miner`)

Go package: given a broadcast recording of a single radio channel, find repeating segments,
group them into clusters, and output representatives.

## Definitions

- **Broadcast** — a continuous audio recording of a channel's airing over a period.
- **Segment** — a section of the broadcast with a start and an end.
- **Repeat** — a segment that airs more than once (the same audio).
- **Occurrence** — a single appearance of a repeat in the broadcast (a position in time).
- **Cluster** — the group of all occurrences of the same segment.
- **Representative** — an audio sample of the cluster.
- **Spot** — an advertising segment (the unit the search is performed for).
- **Jingle** — a short musical station ident (call sign, station imaging).
- **Stinger** — a short audio separator between broadcast blocks.

Jingles, stingers and music beds also repeat, but they are not spots.

---

## Task

Given the broadcast recording:

1. find all repeats;
2. group the occurrences of each repeat into a cluster;
3. for each cluster, output a representative and the list of occurrences with their time positions;
4. separate spots from non-spot repeats (jingles, stingers, music).

---

## Input

- A local list of broadcast audio files for the period (format set by a parameter).
- Call parameters.
- **For testing:** two months of broadcast from a single radio channel + a list of spots with
  their actual airings (time, spot).

File delivery, writing results to a DB, and period selection are not part of the input — the
file list arrives ready-made.

## Output

- A list of clusters. For each cluster: a representative, occurrences with time positions,
  and a "spot / non-spot repeat" flag.
- The output format is up to the implementation.

---

## Constraints

- **Memory:** one month of one channel is processed with peak RSS ≤ 10 GB. Exceeding it / OOM
  is not acceptable.
- **Scale:** the method must work on two months of broadcast, not just on a short excerpt
  (the hard 10 GB ceiling applies to one month; for two months, report the memory usage, but it
  must not grow unboundedly with the window length).
- **Fingerprinting methods (landmark / peak-pair / Haitsma–Kalker and their variations) must
  not be used.**
- **Robustness:** occurrences of the same spot at different volume levels, with slight edge
  trimming, and with different surrounding content fall into one cluster rather than splitting.
- **Determinism:** the same input produces the same set of clusters. Stochastic methods — with
  fixed seeds and reproducible results.
- A single corrupted/unreadable file does not crash processing.
- All calls to external utilities — with a timeout and proper process termination.

---

## Acceptance

All values must be measured.

1. **Recall against the list.** The share of spot airings from the provided list over two
   months that are covered by clusters (an airing is covered if it falls into a cluster).
   Report as a number. The list does not cover all of the channel's spots; clusters outside
   the list are not counted as false positives.
2. **Fragmentation.** Number of clusters per spot from the list. Target — 1.
3. **Filtering of non-spot repeats.** On a random sample of clusters outside the list — the
   share of genuinely repeating content; the "spot / non-spot repeat" flag is checked on this
   sample.
4. **Memory.** Peak RAM usage on one month of one channel ≤ 10 GB, measured via cgroup (peak,
   not average). For two months — the run completes, report the peak.
5. **Determinism.** Two runs on the same input produce an identical set of clusters.
6. Corrupted file: processing does not crash, the file is skipped, everything else is
   assembled, temporary files are cleaned up.

Mandatory deliverable in addition to the code: a design note covering the decisions listed
below, with the cost of each one stated.

`go test ./...` passes.

---

## Decisions to make and justify

For each item — the chosen solution and what it sacrifices.

1. Repeat-detection method (non-fingerprinting).
2. How the method fits into 10 GB for one month and scales to two months.
3. Criterion for assigning occurrences to the same cluster (similarity threshold; not
   splitting one spot into several clusters and not merging two similar ones).
4. Determining segment boundaries when the surrounding content differs.
5. Criterion for distinguishing a spot from a jingle / stinger / music.
6. Ensuring determinism when using a stochastic method.

---

## Scope (what miner does not do)

Identifying what exactly the found segment is (beyond the "spot / non-spot repeat" flag) ·
writing results to storage · selecting files and the processing period.
