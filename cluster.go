package miner

import "sort"

// Clustering: from confirmed pairwise matches to clusters of airings.
//
// The naive way - one node per matched interval, union across matches - breaks
// on the commonest real-world shape: two spots that usually air back to back.
// Matches between two paired airings cover both spots as one 60 s stretch,
// matches against a solo airing cover 30 s, and a coverage rule then splits
// one spot's airings into a "paired" cluster and a "solo" cluster.
//
// So the timeline is first cut at every boundary any match reported, into
// atomic segments. A match unites each atomic segment on its A side with the
// segment at the same position on its B side. A paired airing is then two
// atomic segments, each joining its own spot's cluster; the 60 s pair is not a
// cluster at all, and neither spot fragments. Adjacent clusters whose airings
// ALWAYS coincide are merged afterwards - that is a spot with a spurious cut
// inside it, or two spots that never air apart and are therefore one spot for
// every purpose this tool has.

// longRepeatFrames is the length past which a repeat is not a spot and is not
// joined to an always-adjacent neighbour.
const longRepeatFrames = int(spotMaxSec * FPS)

// boundaryTol is how close two reported boundaries must be to count as the
// same cut. Half a second is the index step; two airings of one spot report
// their edges within it.
const boundaryTol = FPS / 2

// segment is an atomic stretch of the timeline covered by at least one match.
type segment struct {
	Start, End int
}

func (s segment) Len() int { return s.End - s.Start }

// clusterSet is the intermediate result of clustering.
type clusterSet struct {
	segs   []segment
	parent []int
}

func (c *clusterSet) find(i int) int {
	for c.parent[i] != i {
		c.parent[i] = c.parent[c.parent[i]]
		i = c.parent[i]
	}
	return i
}

func (c *clusterSet) union(a, b int) {
	a, b = c.find(a), c.find(b)
	if a == b {
		return
	}
	// Deterministic: the lower index is the root.
	if a > b {
		a, b = b, a
	}
	c.parent[b] = a
}

// internalHostMin is the length from which a match can host internal repeats:
// a song, a programme segment. Spots are shorter than this.
const internalHostMin = 60 * FPS

// dropInternalRepeats removes matches that are the internal periodicity of a
// longer repeat.
//
// A song played twice is one long match at one lag. Its chorus, sung twice per
// play, also matches ACROSS the plays at that lag plus or minus a chorus
// period - and those extra matches are real, the audio is the same. But they
// are not airings of anything; they are the song's own structure, and left in
// they cut both plays at every chorus, so the verses come out as 30-second
// clusters of two airings an hour apart that look exactly like spots. A match
// whose both sides lie inside a match at least half again as long, at a
// different lag, is dropped: it is that longer repeat's structure.
func dropInternalRepeats(matches []match) []match {
	var hosts []match
	for _, m := range matches {
		if m.Len() >= internalHostMin {
			hosts = append(hosts, m)
		}
	}
	if len(hosts) == 0 {
		return matches
	}
	sort.Slice(hosts, func(a, b int) bool { return hosts[a].AStart < hosts[b].AStart })
	starts := make([]int, len(hosts))
	maxLen := 0
	for i, h := range hosts {
		starts[i] = h.AStart
		if h.Len() > maxLen {
			maxLen = h.Len()
		}
	}
	kept := matches[:0]
	for _, m := range matches {
		internal := false
		lo := sort.SearchInts(starts, m.AStart-maxLen)
		for k := lo; k < len(hosts) && hosts[k].AStart <= m.AStart; k++ {
			h := hosts[k]
			if h.AEnd < m.AEnd || h.Len() < m.Len()*3/2 || absInt(h.Lag-m.Lag) <= boundaryTol {
				continue
			}
			if h.BStart() <= m.BStart() && m.BEnd() <= h.BEnd() {
				internal = true
				break
			}
		}
		if !internal {
			kept = append(kept, m)
		}
	}
	return kept
}

var debugCuts func(pos int, ids []int32, inside bool)

// buildClusters turns matches into clusters of atomic segments.
func buildClusters(matches []match, minLen int) [][]segment {
	if len(matches) == 0 {
		return nil
	}
	matches = dropInternalRepeats(matches)
	cuts := collectBoundaries(matches)
	segs := atomicSegments(matches, cuts)
	cs := &clusterSet{segs: segs, parent: make([]int, len(segs))}
	for i := range cs.parent {
		cs.parent[i] = i
	}

	// Unite A-side segments with their B-side counterparts.
	starts := make([]int, len(segs))
	for i, s := range segs {
		starts[i] = s.Start
	}
	for _, m := range matches {
		lo := sort.SearchInts(starts, m.AStart-boundaryTol)
		for i := lo; i < len(segs) && segs[i].Start < m.AEnd-boundaryTol; i++ {
			if segs[i].End > m.AEnd+boundaryTol {
				continue
			}
			j := segmentAt(segs, starts, segs[i].Start-m.lagAt(segs[i].Start), segs[i].End-m.lagAt(segs[i].End-1))
			if j >= 0 {
				cs.union(i, j)
			}
		}
	}

	groups := map[int][]int{}
	for i := range segs {
		r := cs.find(i)
		groups[r] = append(groups[r], i)
	}
	roots := make([]int, 0, len(groups))
	for r := range groups {
		roots = append(roots, r)
	}
	sort.Ints(roots)

	var out [][]segment
	for _, r := range roots {
		var g []segment
		for _, i := range groups[r] {
			g = append(g, segs[i])
		}
		sort.Slice(g, func(a, b int) bool { return g[a].Start < g[b].Start })
		out = append(out, g)
	}
	out = mergeAlwaysAdjacent(out)

	var kept [][]segment
	for _, g := range out {
		if len(g) >= 2 && medianLen(g) >= minLen {
			kept = append(kept, g)
		}
	}
	sort.Slice(kept, func(a, b int) bool {
		if kept[a][0].Start != kept[b][0].Start {
			return kept[a][0].Start < kept[b][0].Start
		}
		return medianLen(kept[a]) > medianLen(kept[b])
	})
	return kept
}

// collectBoundaries gathers every start and end any match reported, on both
// sides, merges those within boundaryTol into one cut, and keeps a cut only
// where the evidence for it is more than one match's word.
//
// A single match ending early inside a spot - a quiet passage that two mp3
// encodes rendered differently, a lapse just past the dip tolerance - reports
// a boundary that no other match sees. Left in, it cuts every airing of that
// spot in two, and the halves no longer share their neighbours' counts. So a
// cut strictly inside other matches' intervals needs at least two votes behind
// it. A cut at the edge of everything covered needs only one: nothing else
// could have voted for it.
//
// Votes travel along matches. A spot that airs alone once and three times in
// front of the same song ends, on its solo airing, at a cut three matches agree
// on; on each of the paired airings that same cut is one match's endpoint, deep
// inside the 200 s pair matches. It is the same boundary, and the evidence for
// it at one airing is evidence for it at every airing the matches connect it
// to. Each cut's weight is therefore carried across every match that spans it.
func collectBoundaries(matches []match) []int {
	type iv struct{ s, e int }
	// A vote is one match's word for a cut at a position; support is counted
	// in DISTINCT matches, so a match's two endpoints and their carried copies
	// are one piece of evidence. Votes are flat arrays sorted by position -
	// a month has millions of them, and a map per cut is what ran out of memory.
	type vote struct {
		pos int32
		id  int32
	}
	votes := make([]vote, 0, 4*len(matches))
	ivs := make([]iv, 0, 2*len(matches))
	for i, m := range matches {
		id := int32(i)
		votes = append(votes,
			vote{int32(m.AStart), id}, vote{int32(m.AEnd), id},
			vote{int32(m.BStart()), id}, vote{int32(m.BEnd()), id})
		ivs = append(ivs, iv{m.AStart, m.AEnd}, iv{m.BStart(), m.BEnd()})
	}
	sortVotes := func(vs []vote) { sort.Slice(vs, func(a, b int) bool { return vs[a].pos < vs[b].pos }) }

	// cut is a merged position and the ids that voted for it, sorted.
	type cut struct {
		pos int
		ids []int32
	}
	merge := func(vs []vote) []cut {
		sortVotes(vs)
		var out []cut
		for i := 0; i < len(vs); {
			j, sum := i, 0
			for j < len(vs) && vs[j].pos-vs[i].pos <= boundaryTol {
				sum += int(vs[j].pos)
				j++
			}
			ids := make([]int32, 0, j-i)
			for _, v := range vs[i:j] {
				ids = append(ids, v.id)
			}
			sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
			n := 0
			for k, id := range ids {
				if k == 0 || id != ids[k-1] {
					ids[n] = id
					n++
				}
			}
			out = append(out, cut{sum / (j - i), ids[:n]})
			i = j
		}
		return out
	}
	cuts := merge(votes)

	// Carry each cut's voters to the other side of every match that spans it
	// strictly - a match's own endpoints are not carried by that match, which
	// would only count them twice.
	pos := make([]int, len(cuts))
	for i, c := range cuts {
		pos[i] = c.pos
	}
	carried := votes
	for _, m := range matches {
		for _, side := range []struct {
			s, e  int
			shift func(p int) int
		}{
			{m.AStart, m.AEnd, func(p int) int { return p - m.lagAt(p) }},
			{m.BStart(), m.BEnd(), func(p int) int { return p + m.lagAtB(p) }},
		} {
			lo := sort.SearchInts(pos, side.s+boundaryTol+1)
			for k := lo; k < len(cuts) && cuts[k].pos < side.e-boundaryTol; k++ {
				p := int32(side.shift(cuts[k].pos))
				for _, id := range cuts[k].ids {
					carried = append(carried, vote{p, id})
				}
			}
		}
	}
	cuts = merge(carried)

	sort.Slice(ivs, func(a, b int) bool { return ivs[a].s < ivs[b].s })
	strictlyInside := func(p int) bool {
		for _, x := range ivs {
			if x.s > p-boundaryTol {
				break
			}
			if x.e > p+boundaryTol {
				return true
			}
		}
		return false
	}
	var out []int
	for _, c := range cuts {
		if debugCuts != nil {
			debugCuts(c.pos, c.ids, strictlyInside(c.pos))
		}
		if len(c.ids) >= 2 || !strictlyInside(c.pos) {
			out = append(out, c.pos)
		}
	}
	return out
}

// atomicSegments cuts the covered parts of the timeline at the boundaries.
func atomicSegments(matches []match, cuts []int) []segment {
	// Coverage: union of all matched intervals, both sides.
	type iv struct{ s, e int }
	var ivs []iv
	for _, m := range matches {
		ivs = append(ivs, iv{m.AStart, m.AEnd}, iv{m.BStart(), m.BEnd()})
	}
	sort.Slice(ivs, func(a, b int) bool { return ivs[a].s < ivs[b].s })
	var covered []iv
	for _, x := range ivs {
		if n := len(covered); n > 0 && x.s <= covered[n-1].e {
			if x.e > covered[n-1].e {
				covered[n-1].e = x.e
			}
			continue
		}
		covered = append(covered, x)
	}

	var segs []segment
	for _, c := range covered {
		lo := sort.SearchInts(cuts, c.s-boundaryTol)
		prev := -1
		for k := lo; k < len(cuts) && cuts[k] <= c.e+boundaryTol; k++ {
			if prev >= 0 && cuts[k] > prev {
				segs = append(segs, segment{prev, cuts[k]})
			}
			prev = cuts[k]
		}
	}
	return segs
}

// segmentAt finds the atomic segment at [s, e), within tolerance, or -1.
func segmentAt(segs []segment, starts []int, s, e int) int {
	i := sort.SearchInts(starts, s-boundaryTol)
	for ; i < len(segs) && segs[i].Start <= s+boundaryTol; i++ {
		if absInt(segs[i].End-e) <= boundaryTol {
			return i
		}
	}
	return -1
}

// mergeAlwaysAdjacent joins clusters whose airings always abut in the same
// order. Two segments that never appear apart are one repeat that was cut in
// two - by a lapse in agreement, or because the two spots have never aired
// separately, which is the same thing as far as the broadcast can show.
func mergeAlwaysAdjacent(groups [][]segment) [][]segment {
	for {
		merged := false
		// Index every segment by its start and end for adjacency lookup.
		byStart := map[int]int{}
		byEnd := map[int]int{}
		for gi, g := range groups {
			for _, s := range g {
				byStart[s.Start] = gi
				byEnd[s.End] = gi
			}
		}
		for gi := 0; gi < len(groups) && !merged; gi++ {
			g := groups[gi]
			if len(g) == 0 {
				continue
			}
			// Does every segment of g have the SAME cluster immediately after
			// it, and does that cluster have g immediately before every one of
			// its segments?
			next := -1
			ok := true
			for _, s := range g {
				n, found := byStart[s.End]
				if !found || n == gi {
					ok = false
					break
				}
				if next == -1 {
					next = n
				} else if next != n {
					ok = false
					break
				}
			}
			if !ok || next < 0 || len(groups[next]) != len(g) {
				continue
			}
			// A long repeat - a song, a programme segment - that always follows
			// a spot is still a song, and joining them would hand the spot the
			// song's verdict. Only spot-length neighbours are joined; two spots
			// that never air apart are one spot for every purpose here.
			if medianLen(g) >= longRepeatFrames || medianLen(groups[next]) >= longRepeatFrames {
				continue
			}
			for _, s := range groups[next] {
				if p, found := byEnd[s.Start]; !found || p != gi {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			// Merge: each segment of g extends to the end of its follower.
			joined := make([]segment, 0, len(g))
			for _, s := range g {
				n := groups[next][segIndexByStart(groups[next], s.End)]
				joined = append(joined, segment{s.Start, n.End})
			}
			groups[gi] = joined
			groups = append(groups[:next], groups[next+1:]...)
			merged = true
		}
		if !merged {
			return groups
		}
	}
}

func segIndexByStart(g []segment, start int) int {
	for i, s := range g {
		if s.Start == start {
			return i
		}
	}
	return 0
}

func medianLen(g []segment) int {
	ls := make([]int, len(g))
	for i, s := range g {
		ls[i] = s.Len()
	}
	sort.Ints(ls)
	return ls[len(ls)/2]
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
