package miner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The spec's acceptance criteria, measured on a synthetic broadcast with known
// truth. See synth_test.go for what the broadcast contains.

func standardDefs() []spotDef {
	return []spotDef{
		{"spot-A", 101, 30, true},
		{"spot-B", 102, 20, true},
		{"spot-C", 103, 15, true},
		{"spot-D", 104, 45, true},
		{"spot-E", 105, 10, true},
		{"spot-F", 106, 25, true},
		{"ident-1", 201, 4, false},
		{"ident-2", 202, 3.5, false},
	}
}

func mineOpts(t *testing.T, files []Input) Options {
	t.Helper()
	temp := filepath.Join(t.TempDir(), "tmp")
	out := filepath.Join(t.TempDir(), "out")
	for _, d := range []string{temp, out} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The synthetic broadcasts are minutes long, so a spot's airings sit
	// minutes apart; the production default of five minutes between two
	// stretches - which exists to ignore a chorus repeating inside a song -
	// would leave some airings with no partner at all. Real material has hours.
	return Options{Inputs: files, TempDir: temp, OutDir: out, MaxConcurrent: 4, MinLagSec: 30}
}

// Criteria 1, 2 and 3: every airing covered, one cluster per repeat, and the
// spot flag right - on a broadcast where the same spot airs at different
// gains, with its edges trimmed, alone and in blocks, across a file join.
func TestAcceptanceRecallFragmentationAndFlag(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	sb := buildBroadcast(t, dir, 1, 40*60, 15*60, standardDefs(), 5, 0.5)

	res, err := Mine(context.Background(), mineOpts(t, sb.Files))
	if err != nil {
		t.Fatalf("Mine: %v", err)
	}
	ev := evaluate(sb, res)
	t.Logf("%s", ev)
	t.Logf("stats: %+v", res.Stats)
	for _, c := range res.Clusters {
		t.Logf("cluster %d: %.1fs x%d spot=%v (%s)", c.ID, c.DurationSec, len(c.Occurrences), c.IsSpot, c.Reason)
	}

	if ev.Recall < 1.0 {
		t.Errorf("recall %.1f%%: %d of %d airings are in no cluster", ev.Recall*100, ev.Airings-ev.Covered, ev.Airings)
	}
	if ev.Fragmented > 0 {
		t.Errorf("%d repeats were split across clusters: %v", ev.Fragmented, ev.ClustersPer)
	}
	if ev.SpotFlagWrong > 0 {
		t.Errorf("spot flag wrong on %d airings", ev.SpotFlagWrong)
	}
	if ev.Extra > 0 {
		t.Errorf("%d clusters touch no known repeat: the programme was found to repeat itself", ev.Extra)
	}
}

// Criterion 5: two runs give an identical set of clusters.
func TestAcceptanceDeterminism(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	sb := buildBroadcast(t, dir, 2, 20*60, 10*60, standardDefs()[:5], 3, 0.4)

	run := func() []Cluster {
		res, err := Mine(context.Background(), mineOpts(t, sb.Files))
		if err != nil {
			t.Fatal(err)
		}
		return res.Clusters
	}
	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatalf("run 1 found %d clusters, run 2 found %d", len(a), len(b))
	}
	for i := range a {
		if a[i].DurationSec != b[i].DurationSec || len(a[i].Occurrences) != len(b[i].Occurrences) || a[i].IsSpot != b[i].IsSpot {
			t.Fatalf("cluster %d differs between runs:\n%+v\n%+v", i, a[i], b[i])
		}
		for k := range a[i].Occurrences {
			if !a[i].Occurrences[k].Start.Equal(b[i].Occurrences[k].Start) {
				t.Fatalf("cluster %d occurrence %d differs: %s vs %s", i, k, a[i].Occurrences[k].Start, b[i].Occurrences[k].Start)
			}
		}
	}
}

// Criterion 6: a corrupt file is skipped, everything else is processed, and
// the scratch directory is left clean.
func TestAcceptanceCorruptFileIsSkipped(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	sb := buildBroadcast(t, dir, 3, 12*60, 6*60, standardDefs()[:3], 3, 0.3)
	bad := filepath.Join(dir, "broken.mp3")
	if err := os.WriteFile(bad, []byte("this is not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := append(sb.Files, Input{Path: bad, Start: sb.Files[len(sb.Files)-1].Start.Add(6 * time.Minute)})

	opt := mineOpts(t, files)
	res, err := Mine(context.Background(), opt)
	if err != nil {
		t.Fatalf("one corrupt file failed the run: %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Path != bad {
		t.Fatalf("Skipped = %+v, want the corrupt file alone", res.Skipped)
	}
	if res.Stats.Files != len(sb.Files) {
		t.Errorf("processed %d files, want %d", res.Stats.Files, len(sb.Files))
	}
	ev := evaluate(sb, res)
	if ev.Recall < 1.0 {
		t.Errorf("recall %.1f%% with one corrupt file present", ev.Recall*100)
	}
	entries, _ := os.ReadDir(opt.TempDir)
	if len(entries) != 0 {
		t.Errorf("scratch not cleaned up: %d entries", len(entries))
	}
}
