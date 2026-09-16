package miner

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/radioenerji/miner/internal/ff"
)

// TestFragmentation asks the miner about its own output: the representative
// airing of every cluster is cut out of the audio, the excerpts are laid on a
// timeline with a break between each, and mined again. Two representatives
// that repeat each other are the same audio that came out as two clusters -
// fragmentation the spot list would count against us, measured without a spot
// list. A diagnostic, run by hand:
//
//	FRAG=var/month-server.json FRAG_IN=MediaforCheck go test -run TestFragmentation -v -timeout 2h
//
// The result's paths may come from another machine; the last two path elements
// (day directory, file) are resolved under FRAG_IN.
func TestFragmentation(t *testing.T) {
	resPath := os.Getenv("FRAG")
	if resPath == "" {
		t.Skip("set FRAG=path/to/clusters.json and FRAG_IN=dir")
	}
	var res Result
	b, err := os.ReadFile(resPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("FRAG_IN")
	local := func(p string) string {
		parts := strings.Split(filepath.ToSlash(p), "/")
		return filepath.Join(root, parts[len(parts)-2], parts[len(parts)-1])
	}
	onlySpots := os.Getenv("FRAG_ALL") == ""

	// Which excerpt of which file each cluster needs.
	type want struct {
		cluster int // index into res.Clusters
		off     float64
		dur     float64
	}
	byFile := map[string][]want{}
	for i, c := range res.Clusters {
		if onlySpots && !c.IsSpot {
			continue
		}
		o := representativeOf(c)
		byFile[local(o.File)] = append(byFile[local(o.File)], want{i, o.OffsetSec, c.DurationSec})
	}
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)
	t.Logf("%d clusters, representatives in %d files", countWants(byFile), len(files))

	// Decode each file once, keep only the excerpts. Twenty minutes of
	// decoding on a laptop, so the excerpts are cached beside the result.
	excerpt := make([][]Frame, len(res.Clusters))
	cache := resPath + ".excerpts"
	if f, err := os.Open(cache); err == nil {
		err = gob.NewDecoder(f).Decode(&excerpt)
		f.Close()
		if err != nil || len(excerpt) != len(res.Clusters) {
			excerpt = make([][]Frame, len(res.Clusters))
		} else {
			files = nil
		}
	}
	r := ff.NewRunner("ffmpeg", "ffprobe", 0, 4)
	var mu sync.Mutex
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	done := 0
	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(f string) {
			defer wg.Done()
			defer func() { <-sem }()
			fr, _, err := extractFile(context.Background(), r, f)
			mu.Lock()
			defer mu.Unlock()
			done++
			if err != nil {
				t.Logf("%s: %v", f, err)
				return
			}
			for _, w := range byFile[f] {
				a, z := int(w.off*FPS), int((w.off+w.dur)*FPS)
				if a < 0 || z > len(fr) || z <= a {
					continue
				}
				excerpt[w.cluster] = append([]Frame(nil), fr[a:z]...)
			}
			if done%50 == 0 {
				fmt.Fprintf(os.Stderr, "decoded %d/%d files\n", done, len(files))
			}
		}(f)
	}
	wg.Wait()
	if len(files) > 0 {
		if f, err := os.Create(cache); err == nil {
			gob.NewEncoder(f).Encode(excerpt)
			f.Close()
		}
	}

	// One excerpt per "hour", so every join is a break and nothing is adjacent.
	tl := &timeline{}
	t0 := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	placed := 0
	for i, fr := range excerpt {
		if fr == nil {
			continue
		}
		tl.add(fmt.Sprintf("cluster-%d", res.Clusters[i].ID), t0.Add(time.Duration(placed)*time.Hour), fr)
		placed++
	}
	// normalize wants inputs; the timeline is already built.
	opt := Options{Inputs: []Input{{Path: "excerpts"}}, TempDir: t.TempDir(), OutDir: t.TempDir(), MaxConcurrent: 4, MinLagSec: 1}
	if err := opt.normalize(); err != nil {
		t.Fatal(err)
	}
	var stats Stats
	got := mineTimeline(context.Background(), opt, tl, &stats)

	// Report: every group of originals that came out as one cluster.
	type group struct {
		IDs  []int
		Durs []float64
		Ns   []int
		Dur  float64
	}
	var groups []group
	orig := func(path string) *Cluster {
		var id int
		fmt.Sscanf(path, "cluster-%d", &id)
		for i := range res.Clusters {
			if res.Clusters[i].ID == id {
				return &res.Clusters[i]
			}
		}
		return nil
	}
	dupClusters := 0
	for _, c := range got {
		seen := map[int]bool{}
		g := group{Dur: c.DurationSec}
		for _, o := range c.Occurrences {
			oc := orig(o.File)
			if oc == nil || seen[oc.ID] {
				continue
			}
			seen[oc.ID] = true
			g.IDs = append(g.IDs, oc.ID)
			g.Durs = append(g.Durs, oc.DurationSec)
			g.Ns = append(g.Ns, len(oc.Occurrences))
		}
		if len(g.IDs) < 2 {
			continue
		}
		groups = append(groups, g)
		dupClusters += len(g.IDs) - 1
	}
	sort.Slice(groups, func(a, b int) bool { return len(groups[a].IDs) > len(groups[b].IDs) })
	// Sharing a 4 s jingle does not make two 40 s spots the same spot: many
	// creatives of one advertiser share their tag. A cluster is a duplicate
	// only when what it shares is (nearly) all of it, and there is another
	// such cluster in the group to be a duplicate of.
	whole := 0
	wholeGroups := 0
	for _, g := range groups {
		n := 0
		for _, d := range g.Durs {
			if g.Dur >= 0.8*d {
				n++
			}
		}
		if n >= 2 {
			whole += n - 1
			wholeGroups++
		}
	}
	t.Logf("%d representatives mined: %d groups of representatives share audio; %d clusters share something with another",
		placed, len(groups), dupClusters)
	t.Logf("fragmentation: %d clusters are (nearly) wholly the same audio as another cluster, in %d groups", whole, wholeGroups)
	for _, g := range groups[:minInt(40, len(groups))] {
		var parts []string
		for i := range g.IDs {
			parts = append(parts, fmt.Sprintf("%d(%.1fs x%d)", g.IDs[i], g.Durs[i], g.Ns[i]))
		}
		t.Logf("  shared %5.1fs: %s", g.Dur, strings.Join(parts, " "))
	}
	if out := os.Getenv("FRAG_OUT"); out != "" {
		b, _ := json.MarshalIndent(groups, "", " ")
		os.WriteFile(out, b, 0o644)
	}
}

func countWants[K comparable, V any](m map[K][]V) int {
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n
}
