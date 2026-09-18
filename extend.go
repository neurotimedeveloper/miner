package miner

import "sort"

// Boundary extension: after clustering, each cluster's edges are pushed
// outward as far as its airings keep agreeing.
//
// The atomic segmentation places a cut wherever the broadcast shows a
// boundary - and a campaign's shared tail IS a boundary: five creatives end
// with the same 8 s, so the cut between body and tail is real, and the list
// names body plus tail as the ad. The chaining rules put the two back
// together when the junction is firm, and on the real month they were not
// firm often enough: 17 % of the listed airings were found as a body 5-8 s
// short of its tail, another 4 % short of a head. Every one of those
// airings carries the tail; the cluster's own airings are the evidence, and
// this asks them directly. A half-second at a time, an edge moves outward
// while at least extendShare of the airings agree with the reference over
// the next half-second at simHigh. Where the ad ends and what follows varies,
// agreement collapses and the edge stops. Where what follows is the same
// every time - a spot that never airs apart from its partner - the edge goes
// on, and the two are one unit, which is what the broadcast shows.

const (
	// extendShare is the share of a cluster's airings that must agree for
	// an edge to move. Near-unanimity: this is the ad's own extent, which
	// every airing has by definition; the allowance is for noisy airings.
	// Lower, and a partner that follows most airings would be absorbed
	// into the cluster of an ad that also airs without it.
	extendShare = 0.9
	// extendMaxFrames bounds one edge's travel.
	extendMaxFrames = 60 * FPS
)

// extendClusters pushes every cluster's edges outward and returns the
// groups with their segments lengthened. A group's segments all share the
// reference length on entry (refineAirings) and on exit.
//
// An edge never moves into the airing of a LARGER cluster of spot length. A
// spot that always precedes its partner would otherwise grow to cover the
// partner too, and the partner's airings would then lie in two clusters.
// Pieces - a shared tail, a sting - are shorter than a spot and are taken;
// so is a smaller cluster, which is the piece in that relation: an 8 s
// stretch straddling two spots that never air apart is taken into the
// larger of them.
func extendClusters(tl *timeline, groups [][]segment, simHigh float64) [][]segment {
	step := scoreFrames
	type owned struct {
		seg segment
		g   int
	}
	var spots []owned
	for gi, g := range groups {
		if medianLen(g) < int(spotMinSec*FPS) {
			continue
		}
		for _, s := range g {
			spots = append(spots, owned{s, gi})
		}
	}
	sort.Slice(spots, func(a, b int) bool { return spots[a].seg.Start < spots[b].seg.Start })
	spotStarts := make([]int, len(spots))
	for i, o := range spots {
		spotStarts[i] = o.seg.Start
	}
	// insideOtherSpot reports whether [from, from+step) overlaps a
	// spot-length airing of a cluster other than gi.
	insideOtherSpot := func(from, gi int) bool {
		i := sort.SearchInts(spotStarts, from+step)
		for k := i - 1; k >= 0 && k >= i-8; k-- {
			o := spots[k]
			if o.g != gi && len(groups[o.g]) >= len(groups[gi]) && o.seg.End > from && o.seg.Start < from+step {
				return true
			}
		}
		return false
	}
	for gi, g := range groups {
		if len(g) < 2 {
			continue
		}
		ref := referenceOf(g)
		// Offsets of every other airing relative to the reference.
		offs := make([]int, 0, len(g)-1)
		for _, s := range g {
			if s != ref {
				offs = append(offs, s.Start-ref.Start)
			}
		}
		need := int(extendShare*float64(len(offs)) + 0.999)
		agree := func(from int) bool {
			// Do the airings agree with the reference over [from, from+step)?
			if from < 0 || from+step > len(tl.frames) || tl.breakBetween(from-1, from+step-1) || insideOtherSpot(from, gi) {
				return false
			}
			n := 0
			for _, off := range offs {
				a := from + off
				if a < 0 || a+step > len(tl.frames) || tl.breakBetween(a-1, a+step-1) {
					continue
				}
				var sum float64
				for k := 0; k < step; k++ {
					sum += tl.cosine(from+k, a+k)
				}
				if sum/float64(step) >= simHigh {
					n++
				}
			}
			return n >= need
		}
		right := 0
		for right < extendMaxFrames && agree(ref.End+right) {
			right += step
		}
		left := 0
		for left < extendMaxFrames && agree(ref.Start-left-step) {
			left += step
		}
		if left == 0 && right == 0 {
			continue
		}
		out := make([]segment, 0, len(g))
		for _, s := range g {
			ns := segment{s.Start - left, s.End + right}
			if ns.Start < 0 {
				ns.Start = 0
			}
			if ns.End > len(tl.frames) {
				ns.End = len(tl.frames)
			}
			out = append(out, ns)
		}
		groups[gi] = out
	}
	return groups
}
