# miner

Finds the segments of a broadcast that air more than once, groups their airings
into clusters, and says which clusters are spots.

Given a channel's recordings for a period, `miner` reports every repeating
segment as a cluster: a representative audio sample, every airing with its
broadcast time and its position in the source file, and a spot / not-a-spot
verdict with the reason for it. It finds advertising without a reference
library - the repetition is the evidence.

The method is not a fingerprint. See [docs/DESIGN.md](docs/DESIGN.md) for the
six decisions the specification asks for, each with what it costs, and
[docs/SPEC.md](docs/SPEC.md) for the specification itself.

## Requirements

- Go 1.27+
- `ffmpeg` and `ffprobe` on `PATH`

## Build and test

```sh
make build        # -> bin/miner
make test         # full suite, needs ffmpeg (~1 min)
make test-short   # pure logic only, no ffmpeg (~1 s)
make test-race
```

## Usage

```sh
bin/miner -in ./broadcast -out ./out -temp ./scratch -tz UTC
bin/miner -manifest files.json -out ./out -temp ./scratch
```

The directory mode reads each file's broadcast time from its name. Both the
recorder's convention and the merger's output are understood:

```
2026-09-05-08-00-25.836_araz_fm_srv10-61min.mp3
2026-08-30-14-00-00_106fm_merged.flac
```

A manifest states the same thing explicitly:

```json
{"files": [
  {"path": "2026-09-05-08-00-25.836_araz_fm_srv10-61min.mp3", "start": "2026-09-05 08:00:25.836"}
]}
```

Consecutive files that abut in time are treated as one continuous broadcast, so
a spot across an hour boundary is found; a gap between files is a gap.

Output, in `-out`:

- `clusters.json` - the full result: every cluster, airing, verdict and reason,
  plus what the run measured about itself (peak RSS, sizes, counts).
- `report.txt` - the same, for reading.
- `representatives/cluster-NNNN.flac` - one audio sample per cluster, unless
  `-no-representatives`.

Flags worth knowing: `-min-repeat` (default 3 s), `-seed` (default 1; the same
seed gives the same clusters), `-ffmpeg-timeout`, `-max-concurrent`.

## As a library

```go
res, err := miner.Mine(ctx, miner.Options{
    Inputs:  inputs,            // []miner.Input{Path, Start}
    TempDir: "/var/lib/miner/scratch",
    OutDir:  "/var/lib/miner/out",
})
for _, c := range res.Clusters {
    fmt.Println(c.ID, c.DurationSec, len(c.Occurrences), c.IsSpot, c.Reason)
}
```

`Result` comes back whenever anything was processed; a file that cannot be
decoded lands in `Skipped` and the run carries on.

## Measuring the acceptance criteria

The suite builds a synthetic broadcast with known truth - spots at different
gains, trimmed edges, blocks, idents, a corrupt file - and measures recall,
fragmentation, the spot flag, determinism and cleanup on it. `make test -v`
prints the numbers. Against a real spot list, `cmd/miner` writes everything the
comparison needs into `clusters.json`; the comparison itself arrives with the
list.
