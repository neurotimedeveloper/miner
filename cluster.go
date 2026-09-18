package miner

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

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

// cutSupportPerMille is the share of the matches crossing a position that
// must end there for the position to be a boundary, in thousandths.
const cutSupportPerMille = 50

// peakShare is the share of the endpoint events within peakReach of a cut
// that the cut itself must hold. Three real boundaries within three seconds
// still pass; endpoints smeared across a fuzzy repeat do not.
const (
	peakShare = 0.3
	peakReach = 6 * boundaryTol
)

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

var debugCuts func(pos int, keep, inside bool)
var debugChains = func() func(string) {
	if os.Getenv("MINER_DEBUG_CHAINS") == "" {
		return nil
	}
	return func(m string) { fmt.Fprintln(os.Stderr, m) }
}()

// buildClusters turns matches into clusters: atomic groups, assembled into
// the units the broadcast airs. The pure path; mineTimeline inserts the
// audio-backed same-audio join between the two steps.
func buildClusters(matches []match, minLen int) [][]segment {
	return assembleClusters(atomicGroups(matches), minLen, nil)
}

// atomicGroups cuts the timeline at every supported boundary and unites the
// atomic segments across matches: one group per distinct piece of audio.
func atomicGroups(matches []match) [][]segment {
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
		// A segment mostly inside the match is carried across; one that
		// sticks out by up to a fifth is an intact airing matched by a
		// trimmed one, and still the same segment.
		lo := sort.SearchInts(starts, m.AStart-m.Len()/4)
		for i := lo; i < len(segs) && segs[i].Start < m.AEnd-boundaryTol; i++ {
			ov := minInt(segs[i].End, m.AEnd) - maxInt(segs[i].Start, m.AStart)
			if float64(ov) < trimmedShare*float64(segs[i].Len()) {
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
	return out
}

// assembleClusters chains the atomic groups into units and keeps the ones
// that repeat at reportable length. between, if given, runs after the first
// chaining pass: the audio-backed same-audio join, which needs the slivers
// already assembled into tails to see that four tails are one.
func assembleClusters(groups [][]segment, minLen int, between func([][]segment) [][]segment) [][]segment {
	out := chainOnce(groups, boundaryTol, func(g []segment) bool { return len(g) >= 2 && medianLen(g) > boundaryTol })
	if between != nil {
		out = between(out)
	}
	out = chainOnce(out, 2*FPS, func(g []segment) bool { return len(g) >= 2 && medianLen(g) >= minLen })

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
// where the evidence for it is independent.
//
// A match's endpoint is one EVENT with two positions: where the agreement
// stopped on the A side and on the B side. One event says nothing about which
// side caused it. A single match ending early inside a spot - a quiet passage
// two mp3 encodes rendered differently - reports a boundary no other match
// sees, and left in it cuts every airing of that spot in two. Counting
// distinct matches was the first rule, and it failed on the real month: one
// noisy airing of a 44 s spot broke its matches with thirteen partners at the
// same points, thirteen "independent" votes cut the spot into five pieces for
// all 1 020 airings, and recall against the list was 23 %. Those thirteen
// events share one airing. So a cut strictly inside coverage is kept only when
// its events do NOT all share one position besides the cut's own: two
// airings that stop there, against two different partners.
//
// That is deliberately more than one airing's word. A spot that airs a
// thousand times before the same song and once alone is reported as
// spot-plus-song, with the solo airing lost; twice alone, and the two are
// separated. One airing cannot tell a genuine solo from a trimmed or damaged
// one, and the damage case shreds a thousand airings where the solo case
// loses one. A cut at the edge of everything covered needs no support: it cuts
// nothing.
//
// Votes travel along matches. The solo airings end at an edge, and on each
// paired airing that same cut is one match's endpoint deep inside the pair
// matches; the evidence is carried across every match that spans a cut, with
// the event's positions intact, so independence is judged on where the
// evidence came from and not where it was delivered.
func collectBoundaries(matches []match) []int {
	type iv struct{ s, e int }
	// A vote is one event's word for a cut at pos; near and far are the
	// event's two positions. Flat arrays sorted by position - a month has
	// millions of them.
	type vote struct {
		pos, near, far int32
	}
	votes := make([]vote, 0, 4*len(matches))
	ivs := make([]iv, 0, 2*len(matches))
	for _, m := range matches {
		as, ae, bs, be := int32(m.AStart), int32(m.AEnd), int32(m.BStart()), int32(m.BEnd())
		votes = append(votes,
			vote{as, as, bs}, vote{ae, ae, be},
			vote{bs, bs, as}, vote{be, be, ae})
		ivs = append(ivs, iv{m.AStart, m.AEnd}, iv{m.BStart(), m.BEnd()})
	}
	sortVotes := func(vs []vote) { sort.Slice(vs, func(a, b int) bool { return vs[a].pos < vs[b].pos }) }

	// cut is a merged position and the events behind it, each once.
	type cut struct {
		pos   int
		votes []vote
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
			ev := append([]vote(nil), vs[i:j]...)
			sort.Slice(ev, func(a, b int) bool {
				if ev[a].near != ev[b].near {
					return ev[a].near < ev[b].near
				}
				return ev[a].far < ev[b].far
			})
			n := 0
			for k, v := range ev {
				if k > 0 && v.near == ev[k-1].near && v.far == ev[k-1].far {
					continue
				}
				ev[n] = v
				n++
			}
			out = append(out, cut{sum / (j - i), ev[:n]})
			i = j
		}
		return out
	}
	cuts := merge(votes)

	// Carry each cut's events to the other side of every match that spans it
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
				for _, v := range cuts[k].votes {
					carried = append(carried, vote{p, v.near, v.far})
				}
			}
		}
	}
	cuts = merge(carried)

	// spanning counts the match intervals that strictly contain p. With
	// every interval longer than the window, that is the intervals begun
	// before it minus the intervals ended before it: two sorted arrays.
	startsSorted := make([]int, len(ivs))
	endsSorted := make([]int, len(ivs))
	for i, x := range ivs {
		startsSorted[i], endsSorted[i] = x.s, x.e
	}
	sort.Ints(startsSorted)
	sort.Ints(endsSorted)
	spanning := func(p int) int {
		begun := sort.SearchInts(startsSorted, p-boundaryTol+1)
		ended := sort.SearchInts(endsSorted, p+boundaryTol+1)
		return begun - ended
	}
	// independent reports whether the events behind a cut at own are more
	// than one airing's doing. Every local event has own as one of its
	// positions, naturally; dependent events all share some OTHER position -
	// the one damaged airing every one of them broke against.
	same := func(a, b int32) bool { return absInt(int(a-b)) <= boundaryTol }
	independent := func(ev []vote, own int32) bool {
		if len(ev) < 2 {
			return false
		}
		for _, anchor := range []int32{ev[0].near, ev[0].far} {
			if same(anchor, own) {
				continue
			}
			shared := true
			for _, v := range ev[1:] {
				if !same(v.near, anchor) && !same(v.far, anchor) {
					shared = false
					break
				}
			}
			if shared {
				return false
			}
		}
		return true
	}
	// A boundary is a PEAK of endpoint density, not merely a count. A live
	// announcement over a fixed music bed - the same nine seconds six
	// hundred times, with a different voice each time - matches its other
	// airings at a lower similarity, and the matches end wherever the
	// voice happened to disagree: endpoints scattered over every
	// half-second of it, each bin with enough events to pass every other
	// test, and the announcement came out as slivers. Endpoints at a real
	// boundary concentrate; a cut has to hold peakShare of the endpoint
	// events within peakReach of it.
	cutVotes := make([]int, len(cuts))
	cutPos := make([]int, len(cuts))
	prefix := make([]int, len(cuts)+1)
	for i, c := range cuts {
		cutVotes[i] = len(c.votes)
		cutPos[i] = c.pos
		prefix[i+1] = prefix[i] + cutVotes[i]
	}
	neighbourhood := func(i int) int {
		lo := sort.SearchInts(cutPos, cutPos[i]-peakReach)
		hi := sort.SearchInts(cutPos, cutPos[i]+peakReach+1)
		return prefix[hi] - prefix[lo]
	}
	var out []int
	for i, c := range cuts {
		span := spanning(c.pos)
		inside := span > 0
		// Support must also be more than a coincidence at scale: a spot
		// aired a thousand times has thousands of matches through every
		// frame of it, and two of them ending in the same half-second
		// somewhere inside is a certainty, not a boundary. A boundary's
		// events are a share of the matches that cross it.
		need := maxInt(2, (span*cutSupportPerMille+999)/1000)
		keep := !inside || (len(c.votes) >= need &&
			float64(len(c.votes)) >= peakShare*float64(neighbourhood(i)) &&
			independent(c.votes, int32(c.pos)))
		if debugCuts != nil {
			debugCuts(c.pos, keep, inside)
		}
		if keep {
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

// unionTol is how far the two sides' segments may disagree at an edge and
// still be the same segment: the cut tolerance, or a twentieth of the
// length. Edges are placed by votes and a soft edge - a fade, a breath -
// lands within a second or so either side; a 30 s spot whose two airings
// were cut 0.7 s apart is one spot, and refineAirings lines the airings up
// afterwards. A twentieth keeps a segment from uniting with one that
// contains it.
func unionTol(length int) int {
	return maxInt(boundaryTol, length/20)
}

// trimmedShare is how much of a segment a trimmed airing of it must still
// hold to count as an airing of it. The same share as mergeSameAudio uses.
const trimmedShare = 0.8

// segmentAt finds the atomic segment at [s, e), within tolerance, or -1.
//
// Failing an exact fit, a segment that contains [s, e) or sits inside it,
// holding at least trimmedShare of the longer, is the same repeat with one
// airing trimmed: a presenter talking over the first two seconds leaves an
// airing that matches from its third second on. One such airing must not cut
// the others (collectBoundaries), so it has to join them here instead.
func segmentAt(segs []segment, starts []int, s, e int) int {
	tol := unionTol(e - s)
	i := sort.SearchInts(starts, s-tol)
	for ; i < len(segs) && segs[i].Start <= s+tol; i++ {
		if absInt(segs[i].End-e) <= tol {
			return i
		}
	}
	// Containing: starts at or before s, ends at or after e.
	long := (e - s) * 5 / 4 // the longest segment [s, e) is trimmedShare of
	i = sort.SearchInts(starts, e-long)
	for ; i < len(segs) && segs[i].Start <= s+tol; i++ {
		sg := segs[i]
		if sg.End >= e-tol && float64(e-s) >= trimmedShare*float64(sg.Len()) {
			return i
		}
	}
	// Contained: inside [s, e), holding most of it.
	i = sort.SearchInts(starts, s-tol)
	for ; i < len(segs) && segs[i].Start < e; i++ {
		sg := segs[i]
		if sg.End <= e+tol && float64(sg.Len()) >= trimmedShare*float64(e-s) {
			return i
		}
	}
	return -1
}

// firmShare is the share of a cluster's airings a relation must hold over
// to count as "always".
const firmShare = 0.7

// maxFamily is how many distinct neighbours a piece may have on the side it
// is joined from. A campaign has a handful of creatives sharing its tail; a
// station sting has as many neighbours as the station has advertisers.
const maxFamily = 8

// assembleChains joins atomic clusters back into the units the broadcast
// airs, wherever a junction is firm.
//
// Atomic segments are the right analysis and the wrong output. A campaign
// with five creatives that share one jingle - "Delux Resi" in five colours,
// "casbak" in four lengths, two "Neotek" separators - comes out of the
// union-find as the jingle (a hundred airings) plus five unique parts (twenty
// each), and the list names five 15 s ads. The pieces have to be put back
// together, per airing, along junctions that hold.
//
// A junction A->B is firm when A is always followed by B and B is a PIECE:
// shorter than a spot, and preceded by only a few distinct things - a family.
// The jingle qualifies: every airing of it follows one of five unique parts.
// The mirror image holds from the right. Each half of the definition earned
// its place on the real month. A station sting that follows most ads is
// "always followed" by whatever comes next, and short; without the family
// condition it chained whole advertising blocks into words and a 44 s spot
// came out as fourteen clusters. "Hilfan boru" (15 s) precedes one of three
// other spots in every airing and is by adjacency alone a left piece of
// each; joined, it came out as three clusters. A jingle is a piece; a spot
// is a spot. And two segments that never appear apart are joined whatever
// their length.
//
// Each airing is walked along its adjacent segments and split at every
// junction that is not firm; the sequence of atomic clusters it traverses is
// its word, and airings with the same word are one output cluster. A repeat
// longer than a spot is never joined to a neighbour: a song that always
// follows a spot is still a song, and joining them would hand the spot the
// song's verdict.
//
// It runs twice. The first pass takes every repeating segment, slivers
// included, with the cut tolerance for a gap: a shared tail can come out of
// the union as a chain of one-second slivers, and this pass puts it back
// together. The second pass takes only segments of reportable length, with a
// two-second gap: a sliver that a stray cut left between a spot and its tail
// - present in fifteen airings, absent in seventeen - would otherwise make
// two words of one spot.
func assembleChains(groups [][]segment, minLen int) [][]segment {
	// A sliver no longer than the cut tolerance is a cut's own imprecision,
	// not a segment: present in some airings of a tail and absent in others,
	// it made two words of one tail. It is skipped, and the gap it leaves is
	// within tolerance.
	first := chainOnce(groups, boundaryTol, func(g []segment) bool { return len(g) >= 2 && medianLen(g) > boundaryTol })
	return chainOnce(first, 2*FPS, func(g []segment) bool { return len(g) >= 2 && medianLen(g) >= minLen })
}

var _ = assembleChains

// chainOnce is one pass of assembleChains over the groups that take part.
func chainOnce(groups [][]segment, gapTol int, takesPart func(g []segment) bool) [][]segment {
	type occ struct {
		seg segment
		g   int
	}
	var all []occ
	var out [][]segment
	for gi, g := range groups {
		if !takesPart(g) {
			out = append(out, g)
			continue
		}
		for _, s := range g {
			all = append(all, occ{s, gi})
		}
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].seg.Start != all[b].seg.Start {
			return all[a].seg.Start < all[b].seg.Start
		}
		return all[a].g < all[b].g
	})
	adjacent := func(i, j int) bool {
		return j < len(all) && absInt(all[j].seg.Start-all[i].seg.End) <= gapTol
	}
	next := map[[2]int]int{}
	for i := range all {
		if adjacent(i, i+1) {
			next[[2]int{all[i].g, all[i+1].g}]++
		}
	}
	long := make([]bool, len(groups))
	for gi, g := range groups {
		long[gi] = medianLen(g) >= longRepeatFrames
	}
	always := func(n, of int) bool { return float64(n) >= firmShare*float64(of) }
	// followedBy[a][b]: a is always followed by b; precededBy[b][a]: b is
	// always preceded by a.
	followedBy := map[int]map[int]int{}
	precededBy := map[int]map[int]int{}
	for k, n := range next {
		a, b := k[0], k[1]
		if always(n, len(groups[a])) {
			if followedBy[a] == nil {
				followedBy[a] = map[int]int{}
			}
			followedBy[a][b] = n
		}
		if always(n, len(groups[b])) {
			if precededBy[b] == nil {
				precededBy[b] = map[int]int{}
			}
			precededBy[b][a] = n
		}
	}
	// rightPiece[b]: b belongs to a family - something is always followed
	// by it, and only a few distinct things ever precede it. leftPiece[a]:
	// the mirror. A campaign's tail follows its handful of creatives; a
	// station sting follows twenty-five different ads, one of which happens
	// to be always followed by it.
	rightPiece := make([]bool, len(groups))
	leftPiece := make([]bool, len(groups))
	preds := make([]int, len(groups))
	succs := make([]int, len(groups))
	for k := range next {
		succs[k[0]]++
		preds[k[1]]++
	}
	for _, m := range followedBy {
		for b := range m {
			rightPiece[b] = preds[b] <= maxFamily
		}
	}
	for _, m := range precededBy {
		for a := range m {
			leftPiece[a] = succs[a] <= maxFamily
		}
	}
	short := make([]bool, len(groups))
	for gi, g := range groups {
		short[gi] = medianLen(g) < int(spotMinSec*FPS)
	}
	// piece reports whether something of length l can be a piece of a host
	// of length host: shorter than the host - a 39 s promo is a 27 s body
	// shared by its dated versions and a 10 s tail of its own, and an 8.1 s
	// stretch that straddles two spots that never air apart is a piece of
	// the 11 s body before it - or shorter than a spot with a host at least
	// half its length: a 0.6 s sliver that always follows a 5 s tail does
	// not claim the tail.
	piece := func(l, host int) bool {
		return l < host || (l < int(spotMinSec*FPS) && 2*host >= l)
	}
	// firm reports how a junction a->b holds: b is a's piece (joinRight), a
	// is b's piece (joinLeft), the two never appear apart (joinBoth), or not
	// at all.
	const (
		joinNone = iota
		joinRight
		joinLeft
		joinBoth
	)
	firm := func(a, b int) int {
		if a == b || long[a] || long[b] {
			return joinNone
		}
		_, ab := followedBy[a][b]
		_, ba := precededBy[b][a]
		la, lb := medianLen(groups[a]), medianLen(groups[b])
		if debugChains != nil && gapTol > boundaryTol && (len(groups[a]) >= 20 || len(groups[b]) >= 20) {
			debugChains(fmt.Sprintf("junction %d(x%d %.1fs)->%d(x%d %.1fs): n %d ab %v ba %v preds %d succs %d short %v/%v", a, len(groups[a]), float64(medianLen(groups[a]))/FPS, b, len(groups[b]), float64(medianLen(groups[b]))/FPS, next[[2]int{a, b}], ab, ba, preds[b], succs[a], short[a], short[b]))
		}
		switch {
		case ab && ba:
			// Two segments that never appear apart, whatever their length:
			// one repeat with a cut inside it, or two spots that have never
			// aired separately, which is the same thing to the broadcast.
			return joinBoth
		case ab && rightPiece[b] && piece(lb, la):
			return joinRight
		case ba && leftPiece[a] && piece(la, lb):
			return joinLeft
		}
		return joinNone
	}

	type word struct {
		key  string
		segs []segment
	}
	byKey := map[string]*word{}
	var order []*word
	for i := 0; i < len(all); {
		j := i
		last := joinNone
		for adjacent(j, j+1) {
			kind := firm(all[j].g, all[j+1].g)
			// A piece is joined to one side. A tail that was claimed by the
			// spot before it is not also the intro of the spot after it;
			// letting it be both chained two ads through their shared
			// sting, and "Neotek" came out as six clusters.
			if kind == joinNone || (kind == joinLeft && last == joinRight) {
				break
			}
			last = kind
			j++
		}
		var sb strings.Builder
		for k := i; k <= j; k++ {
			fmt.Fprintf(&sb, "%d,", all[k].g)
		}
		w := byKey[sb.String()]
		if w == nil {
			w = &word{key: sb.String()}
			byKey[w.key] = w
			order = append(order, w)
		}
		w.segs = append(w.segs, segment{all[i].seg.Start, all[j].seg.End})
		i = j + 1
	}
	for _, w := range order {
		out = append(out, w.segs)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a][0].Start < out[b][0].Start })
	return out
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
