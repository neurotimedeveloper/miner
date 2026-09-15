package miner

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"math/cmplx"

	"github.com/radioenerji/miner/internal/ff"
)

// Feature extraction parameters.
//
// The signal is reduced to a log-mel spectrogram: a small number of bands per
// frame, on a perceptual frequency scale, in decibels. This is the whole
// spectrum every 50 ms rather than a sparse set of peaks, which is what makes
// content with no strong peaks - a music bed under speech, a quiet voice-over -
// visible to the search at all.
const (
	// FeatureRate is the decode rate. Repeats are found on spectral shape below
	// 4 kHz, where mp3 at any bitrate is faithful; higher rates cost memory and
	// add nothing a repeat needs.
	FeatureRate = 8000
	// FrameSize and Hop give 20 frames per second from 64 ms transforms.
	FrameSize = 512
	Hop       = 400
	// SmoothFrames is how many consecutive transforms' power is averaged into
	// one stored frame: ten, so every frame describes 512 ms of audio.
	//
	// The analysis window has to be long. Measured on two captures of one hour
	// through different chains, the same audio agreed over half-second windows
	// on 17 % of the hour with 64 ms frames and on 98 % with 512 ms frames: a
	// short frame is defeated by sub-frame misalignment - two airings' lag is
	// never a whole number of frames - and by fast compressor dynamics. A
	// 4096-point transform per hop gives that window at eighty times the cost
	// of this; averaging ten 512-point power spectra gives the same integration
	// over time, which is the only thing the mel bands keep anyway.
	SmoothFrames = 10
	// FPS is frames per second on the feature timeline.
	FPS = FeatureRate / Hop
	// Bands is the number of mel bands kept per frame.
	Bands = 32

	melLoHz = 100.0
	melHiHz = 3800.0

	// dbStep is the quantisation step of a stored feature value. Half a dB is
	// well under any difference that matters, and it puts a month of one
	// channel at 1.6 GB rather than 13.
	dbStep = 0.5
	// dbFloor bounds the log so digital silence does not become -infinity.
	dbFloor = -80.0

	// cmnHalf is the half-width, in frames, of the running mean removed from
	// every band. Removing the frame's own mean makes a frame gain-invariant;
	// removing the band's running mean over 1.5 s removes the spectral SHAPE
	// the channel, the voice and the processing chain impose on everything -
	// which is what made two unrelated frames of one station's output look 60 %
	// alike. What is left is the part of the spectrum that is moving, and that
	// is the part two airings of the same audio share and two different
	// stretches of programme do not. Measured on the synthetic broadcast, the
	// same-audio/different-audio similarity gap went from 0.65 vs 0.63 to
	// 0.69 vs 0.24 over half-second windows.
	cmnHalf = 15
)

// Frame is one frame of features: Bands values, each in units of dbStep,
// with the frame's own mean across bands removed.
//
// Removing the mean is what makes the features gain-invariant: the same spot
// played 6 dB louder has every band 6 dB higher and the same shape, and the
// shape is all that is stored.
type Frame [Bands]int8

// extractor turns PCM into frames. It owns its FFT scratch, so one is used per
// decode, never shared.
type extractor struct {
	window []float64
	mel    [][]melTap
	buf    []float64
	fft    []complex128
	pend   []float64

	// ring holds the last SmoothFrames mel power spectra; sum is their total.
	ring [SmoothFrames][Bands]float64
	sum  [Bands]float64
	ridx int
	nsub int
}

type melTap struct {
	bin int
	w   float64
}

func newExtractor() *extractor {
	e := &extractor{
		window: make([]float64, FrameSize),
		buf:    make([]float64, FrameSize),
		fft:    make([]complex128, FrameSize),
	}
	for i := range e.window {
		e.window[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(FrameSize-1))
	}
	e.mel = melFilterbank()
	return e
}

// melFilterbank builds triangular filters on the mel scale over FFT bins.
func melFilterbank() [][]melTap {
	hz2mel := func(f float64) float64 { return 2595 * math.Log10(1+f/700) }
	mel2hz := func(m float64) float64 { return 700 * (math.Pow(10, m/2595) - 1) }
	lo, hi := hz2mel(melLoHz), hz2mel(melHiHz)
	edges := make([]float64, Bands+2)
	for i := range edges {
		edges[i] = mel2hz(lo + (hi-lo)*float64(i)/float64(Bands+1))
	}
	binHz := float64(FeatureRate) / float64(FrameSize)
	out := make([][]melTap, Bands)
	for b := 0; b < Bands; b++ {
		l, c, r := edges[b], edges[b+1], edges[b+2]
		for k := 0; k <= FrameSize/2; k++ {
			f := float64(k) * binHz
			var w float64
			switch {
			case f >= l && f <= c && c > l:
				w = (f - l) / (c - l)
			case f > c && f <= r && r > c:
				w = (r - f) / (r - c)
			}
			if w > 0 {
				out[b] = append(out[b], melTap{k, w})
			}
		}
	}
	return out
}

// feed accepts PCM samples and returns every complete frame they yield.
func (e *extractor) feed(samples []float64) []Frame {
	e.pend = append(e.pend, samples...)
	var out []Frame
	for len(e.pend) >= FrameSize {
		if f, ok := e.frame(e.pend[:FrameSize]); ok {
			out = append(out, f)
		}
		e.pend = e.pend[Hop:]
	}
	// Keep the tail compact so a long decode does not grow the buffer.
	if cap(e.pend) > 4*FrameSize {
		e.pend = append(make([]float64, 0, 2*FrameSize), e.pend...)
	}
	return out
}

// frame folds one transform's mel power into the ring and returns the frame
// it completes; the first SmoothFrames-1 transforms complete none.
func (e *extractor) frame(x []float64) (Frame, bool) {
	for i := range e.fft {
		e.fft[i] = complex(x[i]*e.window[i], 0)
	}
	fftInPlace(e.fft)
	for b, taps := range e.mel {
		var p float64
		for _, t := range taps {
			m := cmplx.Abs(e.fft[t.bin])
			p += t.w * m * m
		}
		e.sum[b] += p - e.ring[e.ridx][b]
		e.ring[e.ridx][b] = p
	}
	e.ridx = (e.ridx + 1) % SmoothFrames
	e.nsub++
	if e.nsub < SmoothFrames {
		return Frame{}, false
	}
	var db [Bands]float64
	var mean float64
	for b := 0; b < Bands; b++ {
		v := 10 * math.Log10(e.sum[b]/SmoothFrames+1e-3)
		if v < dbFloor {
			v = dbFloor
		}
		db[b] = v
		mean += v
	}
	mean /= Bands
	var f Frame
	for b := range db {
		q := math.Round((db[b] - mean) / dbStep)
		if q > 127 {
			q = 127
		}
		if q < -127 {
			q = -127
		}
		f[b] = int8(q)
	}
	return f, true
}

// fftInPlace is an iterative radix-2 FFT; FrameSize is a power of two.
func fftInPlace(a []complex128) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for l := 2; l <= n; l <<= 1 {
		ang := -2 * math.Pi / float64(l)
		wl := complex(math.Cos(ang), math.Sin(ang))
		for i := 0; i < n; i += l {
			w := complex(1, 0)
			for j := 0; j < l/2; j++ {
				u, v := a[i+j], a[i+j+l/2]*w
				a[i+j], a[i+j+l/2] = u+v, u-v
				w *= wl
			}
		}
	}
}

// extractFile decodes one file and returns its frames and its decoded sample
// count. The count comes from decoding, never from the container.
func extractFile(ctx context.Context, r *ff.Runner, path string) ([]Frame, int64, error) {
	s, err := r.StreamPCM(ctx, path, FeatureRate)
	if err != nil {
		return nil, 0, err
	}
	defer s.Close()

	ex := newExtractor()
	buf := make([]byte, 2*Hop*50)
	pcm := make([]float64, 0, Hop*50)
	var frames []Frame
	var total int64
	for {
		n, rerr := io.ReadFull(s, buf)
		n -= n % 2
		total += int64(n / 2)
		pcm = pcm[:0]
		for i := 0; i < n; i += 2 {
			pcm = append(pcm, float64(int16(binary.LittleEndian.Uint16(buf[i:])))/32768)
		}
		frames = append(frames, ex.feed(pcm)...)
		if rerr != nil {
			break
		}
	}
	if err := s.Wait(); err != nil {
		return nil, 0, err
	}
	applyCMN(frames)
	return frames, total, nil
}

// applyCMN subtracts each band's running mean over +/-cmnHalf frames.
func applyCMN(frames []Frame) {
	if len(frames) == 0 {
		return
	}
	// Prefix sums per band, so each frame's window mean is O(1).
	n := len(frames)
	var sum [Bands][]int32
	for b := 0; b < Bands; b++ {
		sum[b] = make([]int32, n+1)
		for i, f := range frames {
			sum[b][i+1] = sum[b][i] + int32(f[b])
		}
	}
	out := make([]Frame, n)
	for i := range frames {
		lo, hi := i-cmnHalf, i+cmnHalf+1
		if lo < 0 {
			lo = 0
		}
		if hi > n {
			hi = n
		}
		w := float64(hi - lo)
		for b := 0; b < Bands; b++ {
			m := float64(sum[b][hi]-sum[b][lo]) / w
			q := math.Round(float64(frames[i][b]) - m)
			if q > 127 {
				q = 127
			}
			if q < -127 {
				q = -127
			}
			out[i][b] = int8(q)
		}
	}
	copy(frames, out)
}
