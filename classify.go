package miner

import (
	"fmt"
	"sort"
	"time"
)

// Classification: is this repeat a spot?
//
// Nothing in the audio says "advertisement". What the broadcast does show is
// how a repeat behaves: how long it is, how often it airs, whether it airs in
// company or alone, and whether it airs at the same minute of every hour. Each
// verdict carries its reason, because a flag that cannot be argued with cannot
// be corrected either - and the thresholds here are the ones most worth tuning
// against the real list (decision 5).
const (
	// Spots run from a few seconds to about a minute; station idents and
	// stingers sit under this floor, songs and programme elements over the cap.
	spotMinSec = 8.0
	spotMaxSec = 120.0

	// neighbourGapSec is how close another cluster's airing has to be for two
	// airings to count as adjacent - inside one advertising block.
	neighbourGapSec = 2.0

	// hourlyShare is the share of airings at one minute-of-hour above which a
	// repeat is a scheduled element - a news sting, an hourly ident - rather
	// than a spot placed wherever the sales house sold the slot.
	hourlyShare = 0.8

	// unitShare is the share of two clusters' airings that must follow each
	// other, in both directions, for the two to be pieces of one unit; and
	// unitGapSec how far apart the pieces may sit - a dropout's worth.
	unitShare  = 0.8
	unitGapSec = 5.0
)

// classify sets IsSpot and Reason on every cluster.
func classify(clusters []Cluster) {
	// Every airing of every cluster, sorted, for adjacency lookups.
	var all []placedAiring
	for i, c := range clusters {
		for _, o := range c.Occurrences {
			all = append(all, placedAiring{o.Start, o.End, i})
		}
	}
	sort.Slice(all, func(a, b int) bool { return all[a].start.Before(all[b].start) })
	starts := make([]time.Time, len(all))
	for i, a := range all {
		starts[i] = a.start
	}
	gap := time.Duration(neighbourGapSec * float64(time.Second))
	unitLen := unitLengths(clusters, all, starts)

	for i := range clusters {
		c := &clusters[i]
		n := len(c.Occurrences)
		switch {
		case unitLen[i].sec > spotMaxSec:
			// A song cut into pieces is still a song. A four-minute song came
			// out as 104 s + 145 s on the real month, and a 104 s piece is
			// spot-length; what gives it away is that the pieces never air
			// apart.
			c.IsSpot = false
			c.Reason = fmt.Sprintf("%.1fs, one of %d pieces of a %.0fs unit that always airs as one; a song or a programme element", c.DurationSec, unitLen[i].pieces, unitLen[i].sec)
			continue
		case c.DurationSec < spotMinSec:
			c.IsSpot = false
			c.Reason = fmt.Sprintf("%.1fs is shorter than a spot; an ident, a stinger or a sound effect", c.DurationSec)
			continue
		case c.DurationSec > spotMaxSec:
			c.IsSpot = false
			c.Reason = fmt.Sprintf("%.0fs is longer than a spot; a song or a programme element", c.DurationSec)
			continue
		}

		// Minute-of-hour regularity.
		minutes := map[int]int{}
		for _, o := range c.Occurrences {
			minutes[o.Start.Minute()]++
		}
		top := 0
		for _, k := range minutes {
			if k > top {
				top = k
			}
		}
		if n >= 4 && float64(top)/float64(n) >= hourlyShare {
			c.IsSpot = false
			c.Reason = fmt.Sprintf("%d of %d airings at the same minute of the hour; a scheduled element, not a sold slot", top, n)
			continue
		}

		// Company: how many airings have another cluster's airing right before
		// or after them.
		accompanied := 0
		for _, o := range c.Occurrences {
			lo := sort.Search(len(all), func(k int) bool { return !starts[k].Before(o.Start.Add(-spotMaxSec * time.Second)) })
			found := false
			for k := lo; k < len(all) && !all[k].start.After(o.End.Add(gap)); k++ {
				a := all[k]
				if a.cluster == i {
					continue
				}
				before := a.end.After(o.Start.Add(-gap)) && !a.end.After(o.Start.Add(gap))
				after := !a.start.Before(o.End.Add(-gap)) && !a.start.After(o.End.Add(gap))
				if before || after {
					found = true
					break
				}
			}
			if found {
				accompanied++
			}
		}
		c.IsSpot = true
		if accompanied > 0 {
			c.Reason = fmt.Sprintf("%.1fs, %d airings, %d of them adjacent to another repeat: a spot in an advertising block", c.DurationSec, n, accompanied)
		} else {
			c.Reason = fmt.Sprintf("%.1fs, %d airings, none adjacent to another repeat: spot-length, but airs alone", c.DurationSec, n)
		}
	}
}

// placedAiring is one occurrence with the index of its cluster, for adjacency
// lookups across clusters.
type placedAiring struct {
	start, end time.Time
	cluster    int
}

// unit is what a cluster is a piece of: the summed length of every cluster
// that always airs joined to it, and how many pieces there are.
type unit struct {
	sec    float64
	pieces int
}

// unitLengths finds clusters that always air in the same order, back to back,
// and reports for each cluster the total length of the unit it is a piece of.
//
// "Always" is unitShare of the airings, in both directions: a piece that
// sometimes airs alone is its own repeat, and a spot that opens most
// advertising blocks is not a piece of what follows it.
func unitLengths(clusters []Cluster, all []placedAiring, starts []time.Time) []unit {
	out := make([]unit, len(clusters))
	for i, c := range clusters {
		out[i] = unit{c.DurationSec, 1}
	}
	if len(clusters) == 0 {
		return out
	}
	gap := time.Duration(unitGapSec * float64(time.Second))
	// next[i][j]: how many airings of i are followed within gap by j.
	next := make([]map[int]int, len(clusters))
	for i := range next {
		next[i] = map[int]int{}
	}
	for _, a := range all {
		lo := sort.Search(len(all), func(k int) bool { return !starts[k].Before(a.end.Add(-time.Second)) })
		for k := lo; k < len(all) && !all[k].start.After(a.end.Add(gap)); k++ {
			if all[k].cluster != a.cluster {
				next[a.cluster][all[k].cluster]++
			}
		}
	}
	parent := make([]int, len(clusters))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for i := range clusters {
		ni := len(clusters[i].Occurrences)
		for j, n := range next[i] {
			nj := len(clusters[j].Occurrences)
			if float64(n) >= unitShare*float64(ni) && float64(n) >= unitShare*float64(nj) {
				a, b := find(i), find(j)
				if a != b {
					if a > b {
						a, b = b, a
					}
					parent[b] = a
				}
			}
		}
	}
	sums := map[int]unit{}
	for i, c := range clusters {
		r := find(i)
		u := sums[r]
		u.sec += c.DurationSec
		u.pieces++
		sums[r] = u
	}
	for i := range clusters {
		out[i] = sums[find(i)]
	}
	return out
}
