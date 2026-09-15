package miner

import (
	"math"
	"sort"
	"time"
)

// span is one input laid on the global frame axis.
type span struct {
	Path  string
	Start time.Time
	First int // global index of the file's first frame
	N     int // frames
}

// timeline is every decoded input concatenated onto one frame axis, with the
// places where the axis is NOT continuous recorded.
//
// Consecutive hour files from one recorder are one continuous broadcast and a
// spot may straddle the join; two files with a gap between them are not, and a
// match must not be grown across the gap, because the frames on either side of
// it are not adjacent in broadcast time.
type timeline struct {
	frames []Frame
	norms  []float32 // per-frame vector norm, for cosine
	spans  []span
	breaks []int // global frame indices at which the axis is discontinuous
}

// contiguityTolerance is how far apart a file's end and the next file's start
// may be while still counting as one continuous broadcast.
const contiguityTolerance = time.Second

func (tl *timeline) add(path string, start time.Time, frames []Frame) {
	if len(tl.spans) > 0 {
		prev := &tl.spans[len(tl.spans)-1]
		prevEnd := prev.Start.Add(time.Duration(prev.N) * time.Second / FPS)
		if d := start.Sub(prevEnd); d < -contiguityTolerance {
			// The recorder writes 61-minute files every hour, so consecutive
			// files overlap by a minute. Kept twice, that minute is a repeat of
			// itself at every hour of the month. The previous file's overlap is
			// dropped and the join becomes continuous - the same audio, from
			// the same source, is in the next file.
			overlap := int(float64(-d) / float64(time.Second) * FPS)
			if overlap > prev.N {
				overlap = prev.N
			}
			prev.N -= overlap
			tl.frames = tl.frames[:prev.First+prev.N]
			tl.norms = tl.norms[:prev.First+prev.N]
		} else if d > contiguityTolerance {
			tl.breaks = append(tl.breaks, len(tl.frames))
		}
	}
	first := len(tl.frames)
	tl.spans = append(tl.spans, span{Path: path, Start: start, First: first, N: len(frames)})
	tl.frames = append(tl.frames, frames...)
	for _, f := range frames {
		var s float64
		for _, v := range f {
			s += float64(v) * float64(v)
		}
		tl.norms = append(tl.norms, float32(math.Sqrt(s)))
	}
}

// breakBetween reports whether the axis is discontinuous anywhere in (a, b].
func (tl *timeline) breakBetween(a, b int) bool {
	if a > b {
		a, b = b, a
	}
	i := sort.SearchInts(tl.breaks, a+1)
	return i < len(tl.breaks) && tl.breaks[i] <= b
}

// locate maps a global frame index to its input and broadcast time.
func (tl *timeline) locate(frame int) (path string, at time.Time, offsetSec float64) {
	i := sort.Search(len(tl.spans), func(i int) bool { return tl.spans[i].First+tl.spans[i].N > frame })
	if i >= len(tl.spans) {
		i = len(tl.spans) - 1
	}
	sp := tl.spans[i]
	off := float64(frame-sp.First) / FPS
	return sp.Path, sp.Start.Add(time.Duration(off * float64(time.Second))), off
}

// cosine is the similarity of two frames, 0..1 for typical spectra.
func (tl *timeline) cosine(i, j int) float64 {
	a, b := &tl.frames[i], &tl.frames[j]
	var dot int32
	for k := 0; k < Bands; k++ {
		dot += int32(a[k]) * int32(b[k])
	}
	d := float64(tl.norms[i]) * float64(tl.norms[j])
	if d == 0 {
		return 0
	}
	return float64(dot) / d
}
