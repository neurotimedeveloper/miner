package miner

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/radioenerji/miner/internal/ff"
)

// TestSimTrace prints the windowed similarity along two stretches that are
// supposed to be the same audio, half a second at a time - the way to see WHY
// a match ended where it did. Run by hand:
//
//	TRACE_A=a.mp3 TRACE_OA=1069.85 TRACE_B=b.mp3 TRACE_OB=839.4 TRACE_DUR=260 go test -run TestSimTrace -v
func TestSimTrace(t *testing.T) {
	a, b := os.Getenv("TRACE_A"), os.Getenv("TRACE_B")
	if a == "" || b == "" {
		t.Skip("set TRACE_A, TRACE_OA, TRACE_B, TRACE_OB, TRACE_DUR")
	}
	oa, _ := strconv.ParseFloat(os.Getenv("TRACE_OA"), 64)
	ob, _ := strconv.ParseFloat(os.Getenv("TRACE_OB"), 64)
	dur, _ := strconv.ParseFloat(os.Getenv("TRACE_DUR"), 64)
	r := ff.NewRunner("ffmpeg", "ffprobe", 0, 4)
	fa, _, err := extractFile(context.Background(), r, a)
	if err != nil {
		t.Fatal(err)
	}
	fb, _, err := extractFile(context.Background(), r, b)
	if err != nil {
		t.Fatal(err)
	}
	tl := &timeline{}
	tl.add("a", time.Time{}, fa)
	offB := len(fa)
	tl.add("b", time.Time{}.Add(100*time.Hour), fb)
	ia, ib := int(oa*FPS), int(ob*FPS)
	// Refine the lag within +/- 1 s on the first 10 s.
	bestD, best := 0, -2.0
	for d := -FPS; d <= FPS; d++ {
		var s float64
		for k := 0; k < 10*FPS; k++ {
			s += tl.cosine(ia+k, offB+ib+k+d)
		}
		if s > best {
			best, bestD = s, d
		}
	}
	t.Logf("lag refined by %d frames", bestD)
	n := int(dur * FPS)
	var ring [scoreFrames]float64
	var sum float64
	for k := 0; k < n && ia+k < len(fa) && ib+k+bestD < len(fb); k++ {
		s := tl.cosine(ia+k, offB+ib+k+bestD)
		sum += s - ring[k%scoreFrames]
		ring[k%scoreFrames] = s
		if k%scoreFrames == scoreFrames-1 {
			t.Logf("%6.1fs sim %.2f  norm a %.0f b %.0f", float64(k+1)/FPS, sum/scoreFrames, tl.norms[ia+k], tl.norms[offB+ib+k+bestD])
		}
	}
}
