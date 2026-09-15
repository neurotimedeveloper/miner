package miner

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

	"path/filepath"
	"regexp"

	"github.com/radioenerji/miner/internal/ff"
)

// TestAudit re-measures a finished result against its own audio: every airing
// of every cluster is scored against the cluster's first airing at the best
// lag within the boundary tolerance. A transitive union that chained unlike
// audio shows up as a low minimum. It is a diagnostic, run by hand:
//
//	AUDIT=var/day/clusters.json AUDIT_IN=MediaforCheck/2026-08-01 go test -run TestAudit -v
//
// On the first real day, 215 of 216 clusters scored every airing above 0.5.
func TestAudit(t *testing.T) {
	resPath := os.Getenv("AUDIT")
	if resPath == "" {
		t.Skip("set AUDIT=path/to/clusters.json and AUDIT_IN=dir")
	}
	var res Result
	b, _ := os.ReadFile(resPath)
	json.Unmarshal(b, &res)
	var inputs []Input
	re := regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-(\d{2})-(\d{2})-(\d{2})`)
	paths, _ := filepath.Glob(filepath.Join(os.Getenv("AUDIT_IN"), "*.mp3"))
	sort.Strings(paths)
	for _, p := range paths {
		m := re.FindStringSubmatch(filepath.Base(p))
		st, _ := time.Parse("2006-01-02 15:04:05", m[1]+" "+m[2]+":"+m[3]+":"+m[4])
		inputs = append(inputs, Input{Path: p, Start: st})
	}
	r := ff.NewRunner("ffmpeg", "ffprobe", 0, 4)
	tl := &timeline{}
	for _, in := range inputs {
		fr, _, err := extractFile(context.Background(), r, in.Path)
		if err != nil {
			t.Fatal(err)
		}
		tl.add(in.Path, in.Start, fr)
	}
	frameAt := func(at time.Time) int {
		for _, sp := range tl.spans {
			end := sp.Start.Add(time.Duration(sp.N) * time.Second / FPS)
			if !at.Before(sp.Start) && at.Before(end) {
				return sp.First + int(at.Sub(sp.Start).Seconds()*FPS)
			}
		}
		return -1
	}
	type row struct {
		id              int
		dur             float64
		n               int
		spot            bool
		minSim, meanSim float64
	}
	var rows []row
	for _, c := range res.Clusters {
		f0 := frameAt(c.Occurrences[0].Start)
		L := int(c.DurationSec * FPS)
		if f0 < 0 {
			continue
		}
		minS, sum := 1.0, 0.0
		for _, o := range c.Occurrences[1:] {
			fi := frameAt(o.Start)
			if fi < 0 {
				continue
			}
			// refine +/-3 frames
			best := -1.0
			for d := -15; d <= 15; d++ {
				var s float64
				n := 0
				for k := 5; k < L-5; k++ {
					a, b := f0+k, fi+k+d
					if a < 0 || b < 0 || a >= len(tl.frames) || b >= len(tl.frames) {
						continue
					}
					s += tl.cosine(a, b)
					n++
				}
				if n > 0 && s/float64(n) > best {
					best = s / float64(n)
				}
			}
			if best < minS {
				minS = best
			}
			sum += best
		}
		rows = append(rows, row{c.ID, c.DurationSec, len(c.Occurrences), c.IsSpot, minS, sum / float64(len(c.Occurrences)-1)})
	}
	sort.Slice(rows, func(a, b int) bool { return rows[a].minSim < rows[b].minSim })
	bad := 0
	for _, x := range rows {
		if x.minSim < 0.5 {
			bad++
		}
	}
	t.Logf("%d clusters audited, %d with an airing under 0.5 against the first airing", len(rows), bad)
	for _, x := range rows[:minInt(15, len(rows))] {
		t.Logf("  cluster %4d %6.1fs x%-3d spot=%-5v min=%.2f mean=%.2f", x.id, x.dur, x.n, x.spot, x.minSim, x.meanSim)
	}
}
