package miner

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"
)

var debugMerge = os.Getenv("MINER_DEBUG_MERGE") != ""

// sameAudioShare is how much of the LONGER of two clusters' reference airings
// must be the same audio for the two to be one cluster. Below it they are two
// creatives that share material - many spots of one advertiser share their
// last four seconds; a 44 s spot contains its own 26 s cut-down; a stinger
// sits inside every spot of a block - and stay apart. Measured against the
// shorter instead, the stinger absorbed the spots it sat in.
const sameAudioShare = 0.8

// mergeSameAudio joins clusters whose reference airings are the same audio.
//
// The atomic-segment clustering fragments in ways that are individually rare
// and collectively not: an element whose fade-out puts its end anywhere within
// a second and a half came out as twelve clusters of 8 to 10 s; a spot whose
// long-tagged version aired in one week matched only long airings under the
// per-frame cap and kept its own cluster. Measured on the real month, 287 of
// 6 813 spot clusters were wholly the same audio as another.
//
// So the clusters are checked against each other the way the broadcast was:
// one reference airing per cluster, laid on a timeline with a break between
// each, mined once more. Two references that agree over sameAudioShare of the
// shorter are one repeat; the smaller cluster's airings are re-placed against
// the larger one's reference and join it. A shared tag is far below the share
// and does not join anything.
func mergeSameAudio(ctx context.Context, opt Options, tl *timeline, groups [][]segment) [][]segment {
	if len(groups) < 2 {
		return groups
	}
	// One excerpt per cluster: the reference airing, as refineAirings picks it.
	ex := &timeline{}
	base := make([]int, len(groups)) // excerpt i starts at ex frame base[i]
	t0 := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, g := range groups {
		ref := referenceOf(g)
		base[i] = len(ex.frames)
		ex.add("", t0.Add(time.Duration(i)*time.Hour), tl.frames[ref.Start:ref.End])
	}
	whichExcerpt := func(f int) int {
		return sort.Search(len(base), func(i int) bool { return base[i] > f }) - 1
	}

	o := opt
	o.MinLagSec = 0
	o.DumpMatches = ""
	o.Progress = nil
	var stats Stats
	ms, _ := findMatches(ctx, o, ex, &stats)

	// The longest agreement between each pair of excerpts, and where excerpt
	// j's content sits relative to i's: i's local frame p is j's local p-shift.
	type pair struct {
		i, j   int
		shared int
		shift  int
	}
	best := map[[2]int]pair{}
	for _, m := range ms {
		i, j := whichExcerpt(m.AStart), whichExcerpt(m.BStart())
		if i == j || i < 0 || j < 0 || whichExcerpt(m.AEnd-1) != i || whichExcerpt(m.BEnd()-1) != j {
			continue
		}
		shift := (m.AStart - base[i]) - (m.BStart() - base[j])
		k := [2]int{i, j}
		if p, ok := best[k]; !ok || m.Len() > p.shared {
			best[k] = pair{i, j, m.Len(), shift}
		}
	}
	var pairs []pair
	for _, p := range best {
		pairs = append(pairs, p)
	}
	sort.Slice(pairs, func(a, b int) bool {
		if pairs[a].shared != pairs[b].shared {
			return pairs[a].shared > pairs[b].shared
		}
		if pairs[a].i != pairs[b].i {
			return pairs[a].i < pairs[b].i
		}
		return pairs[a].j < pairs[b].j
	})

	// Union-find with offsets: off[x] is where x's local frame 0 lies in its
	// root's local frames.
	parent := make([]int, len(groups))
	off := make([]int, len(groups))
	members := make([][]int, len(groups))
	for i := range parent {
		parent[i] = i
		members[i] = []int{i}
	}
	find := func(x int) int {
		for parent[x] != x {
			x = parent[x]
		}
		return x
	}
	refLen := func(i int) int { return referenceOf(groups[i]).Len() }
	for _, p := range pairs {
		long := maxInt(refLen(p.i), refLen(p.j))
		if float64(p.shared) < sameAudioShare*float64(long) {
			continue
		}
		ri, rj := find(p.i), find(p.j)
		if ri == rj {
			continue
		}
		// The root with more airings keeps its reference; ties go to the
		// earlier cluster so the result does not depend on pair order.
		ni, nj := len(groups[ri]), len(groups[rj])
		if nj > ni || (nj == ni && rj < ri) {
			// Swap roles: absorb ri's tree into rj. The pair relation is
			// symmetric with the shift negated.
			p.i, p.j, p.shift = p.j, p.i, -p.shift
			ri, rj = rj, ri
		}
		// j's local b is i's local b+shift, which is ri's local b+shift+off[i].
		// rj's local q is j's local q-off[j].
		d := p.shift + off[p.i] - off[p.j]
		if debugMerge {
			fmt.Fprintf(os.Stderr, "merge %d(len %d x%d) <- %d(len %d x%d): shared %d shift %d d %d\n", p.i, refLen(p.i), len(groups[p.i]), p.j, refLen(p.j), len(groups[p.j]), p.shared, p.shift, d)
		}
		for _, x := range members[rj] {
			off[x] += d
			parent[x] = ri
		}
		members[ri] = append(members[ri], members[rj]...)
		members[rj] = nil
	}

	// Rebuild: every absorbed airing re-placed at the root's origin and given
	// the root's length, then refined against the root's reference.
	var out [][]segment
	for r := range groups {
		if parent[r] != r || len(members[r]) == 1 {
			if parent[r] == r {
				out = append(out, groups[r])
			}
			continue
		}
		L := refLen(r)
		var g []segment
		for _, x := range members[r] {
			for _, s := range groups[x] {
				start := s.Start - off[x]
				if start < 0 || start+L > len(tl.frames) || tl.breakBetween(start, start+L-1) {
					continue
				}
				g = append(g, segment{start, start + L})
			}
		}
		sort.Slice(g, func(a, b int) bool { return g[a].Start < g[b].Start })
		if debugMerge {
			n := 0
			for _, x := range members[r] {
				n += len(groups[x])
			}
			fmt.Fprintf(os.Stderr, "root %d: members %v, %d airings in, %d placed\n", r, members[r], n, len(g))
		}
		g = refineAirings(tl, g)
		// Two absorbed airings cannot land on one another - atomic segments
		// are disjoint - but a mis-shift could; keep the first.
		kept := g[:0]
		for _, s := range g {
			if n := len(kept); n > 0 && s.Start < kept[n-1].End-kept[n-1].Len()/2 {
				continue
			}
			kept = append(kept, s)
		}
		if debugMerge && len(kept) != len(g) {
			fmt.Fprintf(os.Stderr, "root %d: %d overlapping airings dropped\n", r, len(g)-len(kept))
		}
		out = append(out, kept)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a][0].Start != out[b][0].Start {
			return out[a][0].Start < out[b][0].Start
		}
		return medianLen(out[a]) > medianLen(out[b])
	})
	return out
}

// referenceOf is the airing a cluster is best represented by: the one whose
// length is closest to the median, earliest on a tie.
func referenceOf(g []segment) segment {
	ref := g[0]
	refLen := medianLen(g)
	for _, s := range g {
		if absInt(s.Len()-refLen) < absInt(ref.Len()-refLen) {
			ref = s
		}
	}
	return ref
}
