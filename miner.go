package miner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
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
	// Go's collector lets the heap grow to twice its live size before it runs.
	// With two gigabytes of features live that is two gigabytes of garbage
	// kept around by default, and the month ran at 9.7 GB against a 10 GB
	// ceiling. A soft limit makes the collector work harder as the limit
	// nears instead; it is a target, not a cap, so the run does not fail if
	// the live data alone exceeds it.
	if opt.MemoryLimitBytes > 0 {
		prev := debug.SetMemoryLimit(opt.MemoryLimitBytes)
		defer debug.SetMemoryLimit(prev)
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
	//
	// Files are decoded in batches of MaxConcurrent, in Start order, and each
	// batch is appended to the timeline before the next begins. Decoding all
	// 744 first and copying afterwards held the month's features twice.
	type decoded struct {
		frames []Frame
		err    error
	}
	// Reserve the timeline for hour-long files up front: untouched pages cost
	// nothing, while letting the slice double as it grows would hold two
	// copies of the features at the last reallocation. Longer files simply
	// grow it.
	reserve := len(inputs) * 3660 * FPS
	tl := &timeline{frames: make([]Frame, 0, reserve), norms: make([]float32, 0, reserve)}
	done := 0
	for batch := 0; batch < len(inputs); batch += opt.MaxConcurrent {
		hi := minInt(batch+opt.MaxConcurrent, len(inputs))
		outs := make([]decoded, hi-batch)
		var wg sync.WaitGroup
		for i := batch; i < hi; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				frames, _, err := extractFile(ctx, runner, inputs[i].Path)
				outs[i-batch] = decoded{frames, err}
			}(i)
		}
		wg.Wait()
		for i := batch; i < hi; i++ {
			in, o := inputs[i], outs[i-batch]
			done++
			opt.progress("features", done, len(inputs))
			if o.err != nil {
				res.Skipped = append(res.Skipped, SkippedFile{Path: in.Path, Reason: o.err.Error()})
				continue
			}
			if len(o.frames) == 0 {
				res.Skipped = append(res.Skipped, SkippedFile{Path: in.Path, Reason: "decoded to no audio"})
				continue
			}
			tl.add(in.Path, in.Start, o.frames)
			res.Stats.Files++
		}
	}
	debug.FreeOSMemory()
	if len(tl.frames) == 0 {
		return res, fmt.Errorf("%w: every input was skipped", ErrNothingToMine)
	}
	res.Clusters = mineTimeline(ctx, opt, tl, &res.Stats)

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

// mineTimeline is everything after decoding: the repeats of a timeline, as
// classified clusters. It is separate from Mine so a timeline assembled some
// other way - a set of excerpts, a fixture - can be mined the same way.
func mineTimeline(ctx context.Context, opt Options, tl *timeline, stats *Stats) []Cluster {
	stats.Frames = len(tl.frames)
	stats.AudioSec = float64(len(tl.frames)) / FPS
	stats.FeatureBytes = int64(len(tl.frames)) * (Bands + 4)

	matches, minLen := findMatches(ctx, opt, tl, stats)
	if opt.DumpMatches != "" {
		dumpMatches(opt.DumpMatches, tl, matches)
	}

	// --- clusters -------------------------------------------------------------
	groups := buildClusters(matches, minLen)
	for i, g := range groups {
		groups[i] = refineAirings(tl, g)
	}
	groups = mergeSameAudio(ctx, opt, tl, groups)
	var clusters []Cluster
	for id, g := range groups {
		c := Cluster{ID: id + 1, DurationSec: float64(medianLen(g)) / FPS}
		for _, s := range g {
			path, at, off := tl.locate(s.Start)
			c.Occurrences = append(c.Occurrences, Occurrence{
				Start: at, End: at.Add(time.Duration(float64(s.Len()) / FPS * float64(time.Second))),
				File: path, OffsetSec: off,
			})
		}
		clusters = append(clusters, c)
	}
	classify(clusters)
	return clusters
}

// findMatches is the search: windows, index, candidates, verification. It
// returns the confirmed matches and the shortest length a repeat may have.
func findMatches(ctx context.Context, opt Options, tl *timeline, stats *Stats) ([]match, int) {
	// --- windows and index ---------------------------------------------------
	basis := newDCTBasis()
	ix := newLSHIndex(opt.Seed)
	nWin := (len(tl.frames)-WindowFrames)/WindowStep + 1
	if nWin < 0 {
		nWin = 0
	}
	descs := make([]QDescriptor, 0, nWin)
	winAt := make([]int, 0, nWin)
	stream := newDescriptorStream(basis, tl.frames)
	for at := 0; at+WindowFrames <= len(tl.frames); at += WindowStep {
		if tl.breakBetween(at, at+WindowFrames-1) {
			continue
		}
		d := stream.at(at)
		ix.hashInto(&d)
		descs = append(descs, quantize(&d))
		winAt = append(winAt, at)
	}
	stats.Windows += len(descs)
	ix.finish()
	opt.progress("index", len(descs), len(descs))
	stats.IndexBytes += int64(len(descs)) * (DescriptorDim + lshTables*8)

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
	stats.SeedPairs += seeds
	stats.VerifiedPairs += verified
	stats.Matches += len(matches)
	return matches, v.minLen
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
func searchAll(ctx context.Context, opt Options, tl *timeline, descs []QDescriptor, winAt []int, ix *lshIndex, basis *dctBasis, v *verifier) ([]match, int, int) {
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
				covered := newCoverage(from, to)
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
						if distanceQ(&d, &descs[cw]) > opt.SeedDistance {
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
	return bridgeDropouts(dedupeMatches(all, tl), tl, v.maxLen), seeds, verified
}

// maxDropoutFrames is the longest hole a bridged match may span.
const maxDropoutFrames = 5 * FPS

// bridgeDropouts joins two matches that are one repeat with a hole in it.
//
// One airing of a four-minute song had lost 1.3 s to a stream dropout. Every
// match between that airing and the other three broke at the dropout into a
// 104 s match and a 145 s match at a lag 1.3 s apart. Three matches, three
// independent-looking votes for a cut at 104 s - and every airing of the song
// was cut in two, on a month where nothing else was wrong with it. The same
// shape cuts a spot when one of its airings has a dropout.
//
// The votes are not independent: they all come from the one damaged airing.
// The evidence for that is the continuation - the same pair of airings agree
// again within a few seconds, at a lag shifted by no more than the hole. Two
// such matches are one match, carrying both lags, and the junction is not a
// boundary anyone voted for.
func bridgeDropouts(ms []match, tl *timeline, maxLen int) []match {
	// ms is sorted by AStart, then Lag.
	used := make([]bool, len(ms))
	out := make([]match, 0, len(ms))
	for i := range ms {
		if used[i] {
			continue
		}
		p := ms[i]
		for {
			best := -1
			bestShift, bestGap := 0, 0
			for j := i + 1; j < len(ms) && ms[j].AStart <= p.AEnd+maxDropoutFrames; j++ {
				q := ms[j]
				if used[j] || q.AStart < p.AEnd-boundaryTol || q.AEnd-p.AStart > maxLen {
					continue
				}
				gapB := q.BStart() - p.BEnd()
				if gapB < -boundaryTol || gapB > maxDropoutFrames {
					continue
				}
				// Not across a discontinuity: the frames either side of a
				// break are not seconds apart in broadcast time.
				if tl.breakBetween(p.AEnd-1, q.AStart) || tl.breakBetween(p.BEnd()-1, q.BStart()) {
					continue
				}
				shift := absInt(q.Lag - p.lagAt(p.AEnd-1))
				// One change of lag per match. A second dropout in the same
				// airing leaves the third piece as its own match.
				if shift > boundaryTol && (p.Split > 0 || q.Split > 0) {
					continue
				}
				gap := q.AStart - p.AEnd
				if best < 0 || shift < bestShift || (shift == bestShift && gap < bestGap) {
					best, bestShift, bestGap = j, shift, gap
				}
			}
			if best < 0 {
				break
			}
			q := ms[best]
			used[best] = true
			p.Sim = (p.Sim*float64(p.Len()) + q.Sim*float64(q.Len())) / float64(p.Len()+q.Len())
			if bestShift > boundaryTol {
				p.Split, p.Lag2 = q.AStart, q.Lag
			} else if q.Split > 0 {
				p.Split, p.Lag2 = q.Split, q.Lag2
			}
			p.AEnd = q.AEnd
		}
		out = append(out, p)
	}
	return out
}

// dedupeMatches merges matches that describe the same repeat at the same lag
// - the ones a chunk boundary let through twice. Never across a break: two
// excerpts laid side by side are not one repeat because their lags agree.
func dedupeMatches(ms []match, tl *timeline) []match {
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
			if lagKey(last.Lag) == lagKey(m.Lag) && m.AStart <= last.AEnd+boundaryTol &&
				!tl.breakBetween(last.AEnd-1, m.AStart) && !tl.breakBetween(last.BEnd()-1, m.BStart()) {
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
	from  int // first query frame this coverage is for
	n     []uint8
	byLag map[int][]segment
}

// maxMatchesPerFrame is how many distinct confirmed matches may explain one
// frame.
const maxMatchesPerFrame = 6

// newCoverage covers query frames [from, to). Coverage is about the query
// side, so a search chunk needs an array the size of its own range, not of the
// month: sixteen workers each holding the whole timeline's worth was 850 MB
// for nothing.
func newCoverage(from, to int) *coverage {
	return &coverage{from: from, n: make([]uint8, to-from), byLag: map[int][]segment{}}
}

func lagKey(lag int) int { return (lag + boundaryTol/2) / boundaryTol }

// has reports whether frame f, paired at lag, needs no further verification.
func (c *coverage) has(f, lag int) bool {
	if i := f - c.from; i >= 0 && i < len(c.n) && c.n[i] >= maxMatchesPerFrame {
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
		if i := f - c.from; i >= 0 && i < len(c.n) && c.n[i] < 255 {
			c.n[i]++
		}
	}
	k := lagKey(m.Lag)
	c.byLag[k] = append(c.byLag[k], segment{m.AStart, m.AEnd})
}

// dumpMatches writes the matches as broadcast times, for reading a run's raw
// evidence by hand: "a_start a_end b_start sim" per line.
func dumpMatches(path string, tl *timeline, ms []match) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	for _, m := range ms {
		_, as, _ := tl.locate(m.AStart)
		_, ae, _ := tl.locate(m.AEnd - 1)
		_, bs, _ := tl.locate(m.AStart - m.Lag)
		fmt.Fprintf(f, "%s %s %s %.3f\n", as.Format("2006-01-02T15:04:05.00"), ae.Format("2006-01-02T15:04:05.00"), bs.Format("2006-01-02T15:04:05.00"), m.Sim)
	}
}
