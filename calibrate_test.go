package miner

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/radioenerji/miner/internal/ff"
)

// TestCalibrate measures the similarity the verifier sees between two
// recordings of the same broadcast through different chains, and between
// unrelated stretches of them. It is how the analysis frame and the thresholds
// were chosen, and how to re-check them when a feature changes:
//
//	MINER_CAL_A=a.mp3 MINER_CAL_B=b.mp3 MINER_CAL_LAG=4.35 go test -run TestCalibrate -v
//
// LAG is B's offset from A in seconds (positive: B's audio starts later).
func TestCalibrate(t *testing.T) {
	a, b := os.Getenv("MINER_CAL_A"), os.Getenv("MINER_CAL_B")
	if a == "" || b == "" {
		t.Skip("set MINER_CAL_A, MINER_CAL_B and MINER_CAL_LAG")
	}
	lagSec, _ := strconv.ParseFloat(os.Getenv("MINER_CAL_LAG"), 64)
	r := ff.NewRunner("ffmpeg", "ffprobe", 0, 4)
	started := time.Now()
	fa, _, err := extractFile(context.Background(), r, a)
	if err != nil {
		t.Fatal(err)
	}
	fb, _, err := extractFile(context.Background(), r, b)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("features for %.1f h in %s", float64(len(fa)+len(fb))/FPS/3600, time.Since(started).Round(time.Second))
	tl := &timeline{}
	tl.add("a", time.Time{}, fa)
	offB := len(fa)
	tl.add("b", time.Time{}.Add(time.Hour*100), fb)

	// Refine the lag to the frame around the stated value: b[i] == a[i+lag].
	approx := int(lagSec * FPS)
	bestLag, best := approx, -2.0
	for lag := approx - FPS; lag <= approx+FPS; lag++ {
		var s float64
		n := 0
		for i := 2000; i < 4000 && i+lag < len(fa) && i < len(fb); i++ {
			s += tl.cosine(i+lag, offB+i)
			n++
		}
		if n > 0 && s/float64(n) > best {
			best, bestLag = s/float64(n), lag
		}
	}
	t.Logf("lag %.2fs (mean frame cosine %.3f)", float64(bestLag)/FPS, best)

	win := func(xs []float64) []float64 {
		var out []float64
		var sum float64
		for i, v := range xs {
			sum += v
			if i >= scoreFrames {
				sum -= xs[i-scoreFrames]
			}
			if i >= scoreFrames-1 {
				out = append(out, sum/scoreFrames)
			}
		}
		return out
	}
	var same []float64
	for i := 100; i+bestLag < len(fa) && i < len(fb)-100; i++ {
		same = append(same, tl.cosine(i+bestLag, offB+i))
	}
	var diff []float64
	for k := 0; k < 3000; k++ {
		x := 100 + (k*7919)%(len(fa)-600)
		y := 100 + (k*104729)%(len(fb)-600)
		if absInt((x-bestLag)-y) < 200 {
			continue
		}
		var s []float64
		for j := 0; j < 40; j++ {
			s = append(s, tl.cosine(x+j, offB+y+j))
		}
		diff = append(diff, win(s)...)
	}
	ws := win(same)
	t.Logf("same audio,   %d-frame window: %s", scoreFrames, quantiles(ws))
	t.Logf("different,    %d-frame window: %s", scoreFrames, quantiles(diff))
	for _, th := range []float64{DefaultSimLow, DefaultSimHigh, 0.7} {
		n := 0
		for _, v := range ws {
			if v >= th {
				n++
			}
		}
		t.Logf("share of the same audio scoring >= %.2f: %.1f%%", th, 100*float64(n)/float64(len(ws)))
	}
}

func quantiles(xs []float64) string {
	c := append([]float64(nil), xs...)
	sort.Float64s(c)
	p := func(f float64) float64 { return c[int(f*float64(len(c)-1))] }
	return fmt.Sprintf("p05=%.2f p25=%.2f p50=%.2f p75=%.2f p95=%.2f", p(.05), p(.25), p(.5), p(.75), p(.95))
}
