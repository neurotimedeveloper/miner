package miner

import (
	"context"
	"testing"
)

// Shapes the random broadcast does not reliably produce, built explicitly.

// Two spots that always air back to back are, as far as the broadcast can
// show, one spot - and one that also airs alone is its own. Neither may
// fragment: the naive "one node per matched interval" clustering split the
// paired airings from the solo ones.
func TestPairedSpotsDoNotFragment(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	defs := []spotDef{{"P", 301, 20, true}, {"Q", 302, 25, true}}
	var plan []airing
	// P+Q together five times...
	for i, at := range []float64{40, 200, 380, 560, 740} {
		plan = append(plan, airing{Name: "P", Start: at, GainDB: float64(i) - 4})
		plan = append(plan, airing{Name: "Q", Start: at + 20.05, GainDB: -float64(i)})
	}
	// ...and Q alone three more times.
	for _, at := range []float64{120, 470, 880} {
		plan = append(plan, airing{Name: "Q", Start: at, GainDB: 2, TrimHead: 0.2})
	}
	sb := renderBroadcast(t, dir, 11, 16*60, 16*60, defs, plan)

	res, err := Mine(context.Background(), mineOpts(t, sb.Files))
	if err != nil {
		t.Fatal(err)
	}
	ev := evaluate(sb, res)
	t.Logf("%s", ev)
	for _, c := range res.Clusters {
		t.Logf("cluster %d: %.1fs x%d spot=%v", c.ID, c.DurationSec, len(c.Occurrences), c.IsSpot)
	}
	if ev.Recall < 1 {
		t.Errorf("recall %.1f%%", ev.Recall*100)
	}
	if ev.Fragmented > 0 {
		t.Errorf("fragmented: %v", ev.ClustersPer)
	}
	var q *Cluster
	for i := range res.Clusters {
		if len(res.Clusters[i].Occurrences) == 8 {
			q = &res.Clusters[i]
		}
	}
	if q == nil {
		t.Fatalf("Q airs 8 times and must be one cluster of 8; got %v", ev.ClustersPer)
	}
	if q.DurationSec < 24 || q.DurationSec > 26 {
		t.Errorf("Q's cluster is %.1fs, want ~25s: the paired airings pulled P into it", q.DurationSec)
	}
}

// Two different spots that end with the same three-second sting must stay two
// clusters. The sting is a repeat of its own; it must not glue them together.
func TestASharedTailDoesNotMergeTwoSpots(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	defs := []spotDef{{"X", 401, 27, true}, {"Y", 402, 27, true}, {"tail", 403, 3, false}}
	var plan []airing
	for i, at := range []float64{30, 150, 290, 420, 560} {
		plan = append(plan, airing{Name: "X", Start: at, GainDB: float64(i)})
		plan = append(plan, airing{Name: "tail", Start: at + 27, GainDB: float64(i)})
	}
	for i, at := range []float64{90, 220, 360, 500, 640} {
		plan = append(plan, airing{Name: "Y", Start: at, GainDB: -float64(i)})
		plan = append(plan, airing{Name: "tail", Start: at + 27, GainDB: -float64(i)})
	}
	sb := renderBroadcast(t, dir, 12, 12*60, 12*60, defs, plan)

	res, err := Mine(context.Background(), mineOpts(t, sb.Files))
	if err != nil {
		t.Fatal(err)
	}
	ev := evaluate(sb, res)
	t.Logf("%s", ev)
	for _, c := range res.Clusters {
		t.Logf("cluster %d: %.1fs x%d spot=%v (%s)", c.ID, c.DurationSec, len(c.Occurrences), c.IsSpot, c.Reason)
	}
	spots := 0
	for _, c := range res.Clusters {
		if c.IsSpot {
			spots++
			if len(c.Occurrences) != 5 {
				t.Errorf("a spot cluster has %d airings, want 5: X and Y were merged through their shared tail", len(c.Occurrences))
			}
		}
	}
	if spots != 2 {
		t.Errorf("%d spot clusters, want 2", spots)
	}
	if ev.Recall < 1 {
		t.Errorf("recall %.1f%%", ev.Recall*100)
	}
}

// Robustness to level: the same spot at -18 dB and at +4 dB is one cluster.
func TestGainDifferencesDoNotSplitASpot(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	defs := []spotDef{{"G", 501, 20, true}}
	var plan []airing
	for i, g := range []float64{0, -18, +4, -12, -6, +2} {
		plan = append(plan, airing{Name: "G", Start: 30 + float64(i)*70, GainDB: g, TrimHead: 0.3 * float64(i%2)})
	}
	sb := renderBroadcast(t, dir, 13, 8*60, 8*60, defs, plan)
	res, err := Mine(context.Background(), mineOpts(t, sb.Files))
	if err != nil {
		t.Fatal(err)
	}
	ev := evaluate(sb, res)
	t.Logf("%s", ev)
	if ev.Recall < 1 || ev.Fragmented > 0 || len(res.Clusters) != 1 {
		t.Errorf("want one cluster covering all six airings; got %d clusters, %s", len(res.Clusters), ev)
	}
}

// A song played three times is a repeat too, and a non-spot. It must not
// swallow the spots around it into one long cluster.
func TestASongIsANonSpotAndSwallowsNothing(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	defs := []spotDef{{"song", 601, 180, true}, {"S", 602, 20, true}}
	var plan []airing
	for i, at := range []float64{30, 400, 800} {
		plan = append(plan, airing{Name: "S", Start: at, GainDB: float64(i)})
		plan = append(plan, airing{Name: "song", Start: at + 20.5, GainDB: -float64(i)})
	}
	// S also airs once on its own; without that the broadcast cannot tell
	// "S then the song" from one 200 s item, and neither can anything else.
	plan = append(plan, airing{Name: "S", Start: 1100, GainDB: -3})
	sb := renderBroadcast(t, dir, 14, 20*60, 20*60, defs, plan)
	// The truth calls the song a "spot" only so the generator renders a voice;
	// what the classifier must say is non-spot.
	res, err := Mine(context.Background(), mineOpts(t, sb.Files))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Clusters {
		t.Logf("cluster %d: %.1fs x%d spot=%v (%s)", c.ID, c.DurationSec, len(c.Occurrences), c.IsSpot, c.Reason)
		switch {
		case c.DurationSec > 100:
			if c.IsSpot {
				t.Errorf("a %.0fs repeat was called a spot", c.DurationSec)
			}
		case c.DurationSec > 15 && c.DurationSec < 25:
			if !c.IsSpot || len(c.Occurrences) != 4 {
				t.Errorf("the 20s spot: spot=%v x%d", c.IsSpot, len(c.Occurrences))
			}
		default:
			t.Errorf("unexpected cluster of %.1fs", c.DurationSec)
		}
	}
	if len(res.Clusters) != 2 {
		t.Errorf("%d clusters, want the song and the spot", len(res.Clusters))
	}
}
