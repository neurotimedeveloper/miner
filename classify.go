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
)

// classify sets IsSpot and Reason on every cluster.
func classify(clusters []Cluster) {
	// Every airing of every cluster, sorted, for adjacency lookups.
	type airing struct {
		start, end time.Time
		cluster    int
	}
	var all []airing
	for i, c := range clusters {
		for _, o := range c.Occurrences {
			all = append(all, airing{o.Start, o.End, i})
		}
	}
	sort.Slice(all, func(a, b int) bool { return all[a].start.Before(all[b].start) })
	starts := make([]time.Time, len(all))
	for i, a := range all {
		starts[i] = a.start
	}
	gap := time.Duration(neighbourGapSec * float64(time.Second))

	for i := range clusters {
		c := &clusters[i]
		n := len(c.Occurrences)
		switch {
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
