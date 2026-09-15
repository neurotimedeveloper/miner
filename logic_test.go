package miner

import (
	"math"
	"math/cmplx"
	"testing"
	"time"
)

// Pure logic, no ffmpeg: fast enough to run on every save.

func TestFFTMatchesANaiveDFT(t *testing.T) {
	const n = 64
	x := make([]complex128, n)
	for i := range x {
		x[i] = complex(math.Sin(float64(i)*0.37)+0.5*math.Cos(float64(i)*1.9), 0)
	}
	got := append([]complex128(nil), x...)
	fftInPlace(got)
	for k := 0; k < n; k++ {
		var want complex128
		for j := 0; j < n; j++ {
			ang := -2 * math.Pi * float64(j*k) / n
			want += x[j] * complex(math.Cos(ang), math.Sin(ang))
		}
		if cmplx.Abs(got[k]-want) > 1e-9 {
			t.Fatalf("bin %d: fft %v, dft %v", k, got[k], want)
		}
	}
}

func TestMelFilterbankCoversTheBandWithoutGaps(t *testing.T) {
	fb := melFilterbank()
	if len(fb) != Bands {
		t.Fatalf("%d filters, want %d", len(fb), Bands)
	}
	covered := map[int]bool{}
	for b, taps := range fb {
		if len(taps) == 0 {
			t.Errorf("band %d has no taps; a band that sees nothing can never differ between two frames", b)
		}
		for _, tp := range taps {
			covered[tp.bin] = true
		}
	}
	binHz := float64(FeatureRate) / float64(FrameSize)
	for k := int(melLoHz/binHz) + 2; k < int(melHiHz/binHz)-2; k++ {
		if !covered[k] {
			t.Errorf("FFT bin %d (%.0f Hz) belongs to no band", k, float64(k)*binHz)
		}
	}
}

// A frame's mean across bands is removed at extraction and each band's running
// mean at CMN; what survives is the moving part of the spectrum, which a gain
// change does not touch.
func TestCMNRemovesAConstantOffsetPerBand(t *testing.T) {
	frames := make([]Frame, 200)
	for i := range frames {
		for b := 0; b < Bands; b++ {
			frames[i][b] = int8(20 + b%3*10) // constant per band
		}
		frames[i][5] += int8(i % 7) // one band that moves
	}
	applyCMN(frames)
	for i := cmnHalf; i < len(frames)-cmnHalf; i++ {
		for b := 0; b < Bands; b++ {
			if b == 5 {
				continue
			}
			if frames[i][b] != 0 {
				t.Fatalf("frame %d band %d = %d after CMN; a constant must vanish", i, b, frames[i][b])
			}
		}
	}
}

func TestTimelineBreaksOnlyAtRealGaps(t *testing.T) {
	tl := &timeline{}
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	hour := make([]Frame, 3600*FPS)
	tl.add("a", base, hour)
	tl.add("b", base.Add(time.Hour), hour)                        // contiguous
	tl.add("c", base.Add(3*time.Hour), hour)                      // an hour missing before it
	tl.add("d", base.Add(4*time.Hour+500*time.Millisecond), hour) // within tolerance
	if tl.breakBetween(3600*FPS-1, 3600*FPS) {
		t.Error("a break between two contiguous hours")
	}
	if !tl.breakBetween(2*3600*FPS-1, 2*3600*FPS) {
		t.Error("no break across a missing hour")
	}
	if tl.breakBetween(3*3600*FPS-1, 3*3600*FPS) {
		t.Error("a break for a half-second join tolerance")
	}
	path, at, off := tl.locate(3*3600*FPS + 10*FPS)
	if path != "d" || off != 10 || !at.Equal(base.Add(4*time.Hour+500*time.Millisecond+10*time.Second)) {
		t.Errorf("locate: %s %s %.1f", path, at, off)
	}
}

// A single early ending inside a spot must not cut every airing of it, but a
// boundary three matches agree on must survive - and must be carried to the
// airings where only one match reports it.
func TestBoundaryVotesDropLoneCutsAndCarryAgreedOnes(t *testing.T) {
	// Three airings of a 600-frame spot at 1000, 5000, 9000; one match between
	// airings 1 and 2 ends 200 frames early.
	ms := []match{
		{AStart: 5000, AEnd: 5400, Lag: 4000}, // early end (lone)
		{AStart: 9000, AEnd: 9600, Lag: 8000},
		{AStart: 9000, AEnd: 9600, Lag: 4000},
	}
	cuts := collectBoundaries(ms)
	has := func(p int) bool {
		for _, c := range cuts {
			if absInt(c-p) <= boundaryTol {
				return true
			}
		}
		return false
	}
	for _, p := range []int{1000, 1600, 5000, 5600, 9000, 9600} {
		if !has(p) {
			t.Errorf("real boundary at %d is missing from %v", p, cuts)
		}
	}
	if has(5400) || has(1400) {
		t.Errorf("a lone early ending survived as a cut: %v", cuts)
	}
}

func TestAlwaysAdjacentClustersAreJoinedUnlessOneIsLong(t *testing.T) {
	// A and B always abut, three times.
	a := []segment{{100, 500}, {2100, 2500}, {4100, 4500}}
	b := []segment{{500, 900}, {2500, 2900}, {4500, 4900}}
	got := mergeAlwaysAdjacent([][]segment{a, b})
	if len(got) != 1 || got[0][0].Len() != 800 {
		t.Fatalf("joined = %v", got)
	}
	// Same, but B is a song: not joined.
	song := []segment{{500, 500 + longRepeatFrames}, {2500, 2500 + longRepeatFrames}, {4500, 4500 + longRepeatFrames}}
	if got := mergeAlwaysAdjacent([][]segment{a, song}); len(got) != 2 {
		t.Fatalf("a song was joined to its neighbour: %v", got)
	}
	// B sometimes airs without A: not joined.
	b2 := append(append([]segment(nil), b...), segment{7000, 7400})
	if got := mergeAlwaysAdjacent([][]segment{a, b2}); len(got) != 2 {
		t.Fatalf("clusters with different airing counts were joined: %v", got)
	}
}

func TestClassifyReasonsByDurationRegularityAndCompany(t *testing.T) {
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	occ := func(dur float64, at ...time.Duration) []Occurrence {
		var out []Occurrence
		for _, a := range at {
			out = append(out, Occurrence{Start: base.Add(a), End: base.Add(a + time.Duration(dur*float64(time.Second)))})
		}
		return out
	}
	cs := []Cluster{
		{ID: 1, DurationSec: 4, Occurrences: occ(4, 0, time.Hour)},
		{ID: 2, DurationSec: 200, Occurrences: occ(200, 5*time.Minute, 2*time.Hour)},
		{ID: 3, DurationSec: 30, Occurrences: occ(30, 10*time.Minute, 70*time.Minute, 130*time.Minute, 190*time.Minute)}, // always minute 10
		{ID: 4, DurationSec: 30, Occurrences: occ(30, 20*time.Minute, 33*time.Minute, 95*time.Minute)},
		{ID: 5, DurationSec: 20, Occurrences: occ(20, 20*time.Minute+30*time.Second, 95*time.Minute+30*time.Second)}, // right after 4
	}
	classify(cs)
	want := []bool{false, false, false, true, true}
	for i, c := range cs {
		if c.IsSpot != want[i] {
			t.Errorf("cluster %d: IsSpot=%v, want %v (%s)", c.ID, c.IsSpot, want[i], c.Reason)
		}
		if c.Reason == "" {
			t.Errorf("cluster %d has no reason", c.ID)
		}
	}
}

func TestOptionsRejectNonsense(t *testing.T) {
	dir := t.TempDir()
	ok := Options{Inputs: []Input{{Path: "x"}}, TempDir: dir, OutDir: dir}
	if err := ok.normalize(); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
	for _, bad := range []Options{
		{Inputs: []Input{{Path: "x"}}, OutDir: dir},                                  // no temp
		{Inputs: []Input{{Path: "x"}}, TempDir: dir, OutDir: dir, MinRepeatSec: 400}, // min > max
		{Inputs: []Input{{Path: "x"}}, TempDir: dir, OutDir: dir, SimHigh: 0.3, SimLow: 0.5},
		{Inputs: nil, TempDir: dir, OutDir: dir},
	} {
		if err := bad.normalize(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// The recorder writes 61-minute files every hour. The overlapping minute must
// not be kept twice, or it is a repeat of itself at every hour of the month.
func TestOverlappingFilesAreJoinedWithoutDuplicatingTheOverlap(t *testing.T) {
	tl := &timeline{}
	base := time.Date(2026, 8, 1, 0, 0, 8, 0, time.UTC)
	file := make([]Frame, 3660*FPS)
	tl.add("h0", base, file)
	tl.add("h1", base.Add(time.Hour), file)
	if got, want := len(tl.frames), (3600+3660)*FPS; got != want {
		t.Fatalf("timeline holds %d frames, want %d: the overlap was kept", got, want)
	}
	if tl.breakBetween(3600*FPS-1, 3600*FPS) {
		t.Error("the join of two overlapping files is a break")
	}
	path, at, _ := tl.locate(3600*FPS + 5)
	if path != "h1" || !at.Equal(base.Add(time.Hour+250*time.Millisecond)) {
		t.Errorf("locate after the join: %s %s", path, at)
	}
}
