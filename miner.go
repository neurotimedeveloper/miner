package miner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/radioenerji/miner/internal/ff"
)

// Mine finds the repeats in the given broadcast.
//
// A Result is returned whenever anything at all was processed; a file that
// cannot be decoded lands in Skipped and the run carries on. An error comes
// back only when the options are unusable, nothing could be processed, or the
// results could not be written.
func Mine(ctx context.Context, opt Options) (Result, error) {
	var res Result
	started := time.Now()
	if err := opt.normalize(); err != nil {
		return res, err
	}
	runner := ff.NewRunner("ffmpeg", "ffprobe", time.Duration(opt.FFmpegTimeoutSec)*time.Second, opt.MaxConcurrent)
	if err := runner.Check(ctx); err != nil {
		return res, err
	}
	work, err := os.MkdirTemp(opt.TempDir, "miner-")
	if err != nil {
		return res, fmt.Errorf("%w: %w", ErrTempDir, err)
	}
	defer os.RemoveAll(work)

	inputs := append([]Input(nil), opt.Inputs...)
	sort.SliceStable(inputs, func(a, b int) bool { return inputs[a].Start.Before(inputs[b].Start) })

	// --- features: every file decoded once, in parallel, added in order ------
	type decoded struct {
		frames []Frame
		err    error
	}
	outs := make([]decoded, len(inputs))
	var wg sync.WaitGroup
	var doneMu sync.Mutex
	done := 0
	for i, in := range inputs {
		wg.Add(1)
		go func(i int, in Input) {
			defer wg.Done()
			frames, _, err := extractFile(ctx, runner, in.Path)
			outs[i] = decoded{frames, err}
			doneMu.Lock()
			done++
			opt.progress("features", done, len(inputs))
			doneMu.Unlock()
		}(i, in)
	}
	wg.Wait()

	// Size the timeline once: growing it by appends would hold two copies of
	// the features at the moment of every reallocation, and features are the
	// largest thing in memory.
	nFrames := 0
	for _, o := range outs {
		nFrames += len(o.frames)
	}
	tl := &timeline{frames: make([]Frame, 0, nFrames), norms: make([]float32, 0, nFrames)}
	for i, in := range inputs {
		if outs[i].err != nil {
			res.Skipped = append(res.Skipped, SkippedFile{Path: in.Path, Reason: outs[i].err.Error()})
			continue
		}
		if len(outs[i].frames) == 0 {
			res.Skipped = append(res.Skipped, SkippedFile{Path: in.Path, Reason: "decoded to no audio"})
			continue
		}
		tl.add(in.Path, in.Start, outs[i].frames)
		res.Stats.Files++
		outs[i].frames = nil
	}
	if len(tl.frames) == 0 {
		return res, fmt.Errorf("%w: every input was skipped", ErrNothingToMine)
	}
	res.Stats.Frames = len(tl.frames)
	res.Stats.AudioSec = float64(len(tl.frames)) / FPS
	res.Stats.FeatureBytes = int64(len(tl.frames)) * (Bands + 4)

	// --- windows and index ---------------------------------------------------
	basis := newDCTBasis()
	var descs []Descriptor
	var winAt []int
	for at := 0; at+WindowFrames <= len(tl.frames); at += WindowStep {
		if tl.breakBetween(at, at+WindowFrames-1) {
			continue
		}
		descs = append(descs, basis.describe(tl.frames, at))
		winAt = append(winAt, at)
	}
	res.Stats.Windows = len(descs)
	ix := newLSHIndex(opt.Seed)
	ix.build(descs)
	opt.progress("index", len(descs), len(descs))
	res.Stats.IndexBytes = int64(len(descs)) * (DescriptorDim*4 + lshTables*8)

	// --- candidates, verified -------------------------------------------------
	v := &verifier{
		tl:      tl,
		simHigh: opt.SimHigh,
		simLow:  opt.SimLow,
		maxDip:  int(opt.MaxDipSec * FPS),
		// A repeat of length L agrees over about L minus half an analysis
		// frame: the frames straddling its edges are half something else.
		minLen:   int(opt.MinRepeatSec*FPS) - SmoothFrames/2,
		maxLen:   int(opt.MaxRepeatSec * FPS),
		lagSlack: WindowStep/2 + 1,
	}
	matches, seeds, verified := searchAll(ctx, opt, tl, descs, winAt, ix, basis, v)
	res.Stats.SeedPairs, res.Stats.VerifiedPairs = seeds, verified
	res.Stats.Matches = len(matches)

	// --- clusters -------------------------------------------------------------
	groups := buildClusters(matches, v.minLen)
	for id, g := range groups {
		g = refineAirings(tl, g)
		c := Cluster{ID: id + 1, DurationSec: float64(medianLen(g)) / FPS}
		for _, s := range g {
			path, at, off := tl.locate(s.Start)
			c.Occurrences = append(c.Occurrences, Occurrence{
				Start: at, End: at.Add(time.Duration(float64(s.Len()) / FPS * float64(time.Second))),
				File: path, OffsetSec: off,
			})
		}
		res.Clusters = append(res.Clusters, c)
	}
	classify(res.Clusters)

	// --- representatives ------------------------------------------------------
	if opt.Representatives {
		if err := os.MkdirAll(filepath.Join(opt.OutDir, "representatives"), 0o755); err != nil {
			return res, fmt.Errorf("%w: %w", ErrOutputIO, err)
		}
		for i := range res.Clusters {
			c := &res.Clusters[i]
			o := representativeOf(*c)
			dst := filepath.Join(opt.OutDir, "representatives", fmt.Sprintf("cluster-%04d.flac", c.ID))
			if err := runner.Extract(ctx, o.File, o.OffsetSec, o.OffsetSec+c.DurationSec, dst); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("cluster %d: representative not written: %v", c.ID, err))
				continue
			}
			c.Representative = dst
		}
	}

	res.Stats.PeakRSSBytes = peakRSS()
	res.Stats.WallSec = time.Since(started).Seconds()
	return res, nil
}

// searchAll runs the candidate search over the whole timeline, in parallel
// chunks of query frames.
//
// Each chunk owns its own coverage: coverage is about the QUERY side, and the
// chunks' query ranges are disjoint, so nothing is shared. A match grown from a
// seed near a chunk's end can reach into the next chunk, whose coverage does
// not know of it; the same repeat can then be found twice with the same lag,
// and those are merged afterwards. Chunks are concatenated in order, so the
// result does not depend on which finished first.
func searchAll(ctx context.Context, opt Options, tl *timeline, descs []Descriptor, winAt []int, ix *lshIndex, basis *dctBasis, v *verifier) ([]match, int, int) {
	total := len(tl.frames)
	workers := opt.MaxConcurrent
	if workers < 1 {
		workers = 1
	}
	// Chunks of an hour, dealt to workers; an hour is long enough that the
	// overlap at chunk ends is negligible and short enough to balance.
	const chunk = FPS * 3600
	nChunks := (total + chunk - 1) / chunk
	type result struct {
		matches         []match
		seeds, verified int
	}
	results := make([]result, nChunks)
	minLag := int(opt.MinLagSec * FPS)

	var wg sync.WaitGroup
	next := make(chan int, nChunks)
	for c := 0; c < nChunks; c++ {
		next <- c
	}
	close(next)
	var progMu sync.Mutex
	done := 0
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream := newDescriptorStream(basis, tl.frames)
			var cands []uint32
			for c := range next {
				if ctx.Err() != nil {
					return
				}
				from, to := c*chunk, minInt((c+1)*chunk, total)
				covered := newCoverage(total)
				var r result
				for q := from; q < to && q+WindowFrames <= total; q++ {
					if tl.breakBetween(q, q+WindowFrames-1) {
						continue
					}
					d := stream.at(q)
					cands = ix.candidates(&d, cands)
					for _, cw := range cands {
						b := winAt[cw]
						if b+WindowFrames > q || q-b < minLag {
							continue // only earlier windows, at least MinLagSec back; each pair once
						}
						if distance(&d, &descs[cw]) > opt.SeedDistance {
							continue
						}
						r.seeds++
						if covered.has(q, q-b) {
							continue
						}
						r.verified++
						m, ok := v.verify(q, b)
						if !ok {
							continue
						}
						r.matches = append(r.matches, m)
						covered.add(m)
						if opt.Debug != nil {
							opt.Debug(m)
						}
					}
				}
				results[c] = r
				progMu.Lock()
				done++
				opt.progress("search", done*chunk, total)
				progMu.Unlock()
			}
		}()
	}
	wg.Wait()

	var all []match
	seeds, verified := 0, 0
	for _, r := range results {
		all = append(all, r.matches...)
		seeds += r.seeds
		verified += r.verified
	}
	return dedupeMatches(all), seeds, verified
}

// dedupeMatches merges matches that describe the same repeat at the same lag
// - the ones a chunk boundary let through twice.
func dedupeMatches(ms []match) []match {
	sort.Slice(ms, func(a, b int) bool {
		if lagKey(ms[a].Lag) != lagKey(ms[b].Lag) {
			return lagKey(ms[a].Lag) < lagKey(ms[b].Lag)
		}
		return ms[a].AStart < ms[b].AStart
	})
	out := ms[:0]
	for _, m := range ms {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if lagKey(last.Lag) == lagKey(m.Lag) && m.AStart <= last.AEnd+boundaryTol {
				if m.AEnd > last.AEnd {
					last.AEnd = m.AEnd
				}
				continue
			}
		}
		out = append(out, m)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].AStart != out[b].AStart {
			return out[a].AStart < out[b].AStart
		}
		return out[a].Lag < out[b].Lag
	})
	return out
}

// refineAirings puts every airing of a cluster exactly where its audio lines
// up with the cluster's reference airing, and gives them all the reference's
// length.
//
// A segment's edges come from boundary votes, which are exact to about half a
// second; the airing's true position is exact to a frame, and it is one lag
// refinement away. Every airing then carries the same length - the segment IS
// one thing - so an airing whose head was trimmed is placed where the segment
// would have started, not where its audio happened to begin.
func refineAirings(tl *timeline, g []segment) []segment {
	if len(g) < 2 {
		return g
	}
	ref := g[0]
	refLen := medianLen(g)
	for _, s := range g {
		if absInt(s.Len()-refLen) < absInt(ref.Len()-refLen) {
			ref = s
		}
	}
	// Score the interior of the reference against a shifted airing.
	lo, hi := ref.Start+scoreFrames, ref.Start+refLen-scoreFrames
	if hi <= lo {
		return g
	}
	out := make([]segment, 0, len(g))
	for _, s := range g {
		if s == ref {
			out = append(out, segment{ref.Start, ref.Start + refLen})
			continue
		}
		bestShift, best := 0, -2.0
		for d := -2 * boundaryTol; d <= 2*boundaryTol; d++ {
			base := s.Start + d - ref.Start
			if lo+base < 0 || hi+base > len(tl.frames) {
				continue
			}
			var sum float64
			for t := lo; t < hi; t++ {
				sum += tl.cosine(t, t+base)
			}
			if sum > best {
				best, bestShift = sum, d
			}
		}
		start := s.Start + bestShift
		if start < 0 {
			start = 0
		}
		end := start + refLen
		if end > len(tl.frames) {
			end = len(tl.frames)
		}
		out = append(out, segment{start, end})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Start < out[b].Start })
	return out
}

// representativeOf picks the airing whose length is closest to the cluster's
// consensus - the one least likely to carry a neighbour's edge.
func representativeOf(c Cluster) Occurrence {
	best := c.Occurrences[0]
	bestD := -1.0
	for _, o := range c.Occurrences {
		d := o.DurationSec() - c.DurationSec
		if d < 0 {
			d = -d
		}
		if bestD < 0 || d < bestD {
			best, bestD = o, d
		}
	}
	return best
}

// coverage decides whether a candidate pair is worth verifying.
//
// Two things are remembered. Per (frame, lag): whether a confirmed match
// already explains this exact pairing - the sixty windows inside one
// thirty-second repeat must not each re-verify the same match, and a repeat
// verified twice counts twice against everything below. Per frame: how many
// DISTINCT matches already explain it. Matching every airing against every
// earlier one is quadratic - a spot that airs 150 times a month is 11 000
// matches, an ident airing sixty times a day 1.7 million - and the clustering
// that followed ran the machine out of memory. Union-find needs one match per
// airing; the boundary evidence needs two independent ones for a cut inside
// other matches, and a stinger shared by two spots needs a few more before its
// own cross-matches get a turn. Each frame is therefore explained by at most
// maxMatchesPerFrame matches.
type coverage struct {
	n     []uint8
	byLag map[int][]segment
}

// maxMatchesPerFrame is how many distinct confirmed matches may explain one
// frame.
const maxMatchesPerFrame = 6

func newCoverage(frames int) *coverage {
	return &coverage{n: make([]uint8, frames), byLag: map[int][]segment{}}
}

func lagKey(lag int) int { return (lag + boundaryTol/2) / boundaryTol }

// has reports whether frame f, paired at lag, needs no further verification.
func (c *coverage) has(f, lag int) bool {
	if c.n[f] >= maxMatchesPerFrame {
		return true
	}
	k := lagKey(lag)
	for _, kk := range []int{k - 1, k, k + 1} {
		for _, s := range c.byLag[kk] {
			if f >= s.Start && f < s.End {
				return true
			}
		}
	}
	return false
}

func (c *coverage) add(m match) {
	for f := m.AStart; f < m.AEnd; f++ {
		if c.n[f] < 255 {
			c.n[f]++
		}
	}
	k := lagKey(m.Lag)
	c.byLag[k] = append(c.byLag[k], segment{m.AStart, m.AEnd})
}
