package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/radioenerji/miner"
)

func writeSummary(w io.Writer, res miner.Result) {
	spots, others := 0, 0
	for _, c := range res.Clusters {
		if c.IsSpot {
			spots++
		} else {
			others++
		}
	}
	fmt.Fprintf(w, "files          %d processed, %d skipped\n", res.Stats.Files, len(res.Skipped))
	fmt.Fprintf(w, "audio          %s\n", (time.Duration(res.Stats.AudioSec) * time.Second).Round(time.Second))
	fmt.Fprintf(w, "clusters       %d  (%d spots, %d other repeats)\n", len(res.Clusters), spots, others)
	fmt.Fprintf(w, "peak rss       %.0f MB\n", float64(res.Stats.PeakRSSBytes)/1e6)
	fmt.Fprintf(w, "took           %.1fs\n", res.Stats.WallSec)
	for _, s := range res.Skipped {
		fmt.Fprintf(w, "skipped: %s: %s\n", s.Path, s.Reason)
	}
	for _, x := range res.Warnings {
		fmt.Fprintf(w, "warning: %s\n", x)
	}
}

// writeReport renders everything a reviewer needs to judge a run: every
// cluster with its airings, its verdict and the reason for it.
func writeReport(w io.Writer, res miner.Result) {
	p := func(f string, a ...any) { fmt.Fprintf(w, f, a...) }
	rule := func() { p("%s\n", strings.Repeat("-", 78)) }
	p("MINER REPORT\n")
	rule()
	writeSummary(w, res)
	p("\nstats: windows %d, seed pairs %d, verified %d, matches %d, features %.0f MB, index %.0f MB\n",
		res.Stats.Windows, res.Stats.SeedPairs, res.Stats.VerifiedPairs, res.Stats.Matches,
		float64(res.Stats.FeatureBytes)/1e6, float64(res.Stats.IndexBytes)/1e6)

	for _, c := range res.Clusters {
		p("\n")
		rule()
		kind := "SPOT"
		if !c.IsSpot {
			kind = "repeat (not a spot)"
		}
		p("cluster %d   %.1fs   x%d   %s\n", c.ID, c.DurationSec, len(c.Occurrences), kind)
		p("  %s\n", c.Reason)
		if c.Representative != "" {
			p("  sample: %s\n", c.Representative)
		}
		for _, o := range c.Occurrences {
			p("  %s  %6.1fs  %s @ %.1fs\n", o.Start.Format("2006-01-02 15:04:05.0"), o.DurationSec(), shortPath(o.File), o.OffsetSec)
		}
	}
}

func shortPath(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
