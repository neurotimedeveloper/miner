package miner

import (
	"math"
	"math/rand"
	"sort"
)

// Window descriptors and the locality-sensitive index over them.
//
// A window is one second of frames. Its descriptor is the low-frequency part
// of a 2-D cosine transform over the 20x32 patch - 4 coefficients along time,
// 8 along frequency - normalised to unit length. Two windows of the same audio
// land within a small distance of each other whatever the gain, the codec, or
// what came before and after; two windows of different audio do not.
//
// The index is a set of seeded random-hyperplane tables over those
// descriptors. It is the one stochastic component of the method, and it is
// stochastic only in that the hyperplanes are drawn once from a fixed seed: the
// same seed gives the same tables, the same candidates, and the same clusters.
const (
	// WindowFrames is one second; WindowStep is half of it, so every second of
	// audio starts a window within 250 ms of its own start.
	WindowFrames = FPS
	WindowStep   = FPS / 2

	dctTime = 4
	dctFreq = 8
	// DescriptorDim is dctTime*dctFreq.
	DescriptorDim = dctTime * dctFreq

	// LSH tables and bits per table. More tables find more candidates; more
	// bits make each bucket purer. 8x20 puts a month of one channel into a few
	// hundred megabytes of index.
	lshTables = 8
	lshBits   = 20

	// maxBucket caps how many windows a bucket may hold before it is ignored.
	// A bucket that big is silence, station noise or a tone - content the
	// index cannot tell apart and the verifier would reject anyway.
	maxBucket = 256
)

// Descriptor is a window's unit-length descriptor.
type Descriptor [DescriptorDim]float32

// dctBasis holds the cosine tables for the 2-D transform.
type dctBasis struct {
	t [dctTime][WindowFrames]float64
	f [dctFreq][Bands]float64
}

func newDCTBasis() *dctBasis {
	b := &dctBasis{}
	for k := 0; k < dctTime; k++ {
		for n := 0; n < WindowFrames; n++ {
			b.t[k][n] = math.Cos(math.Pi / float64(WindowFrames) * (float64(n) + 0.5) * float64(k))
		}
	}
	for k := 0; k < dctFreq; k++ {
		for n := 0; n < Bands; n++ {
			b.f[k][n] = math.Cos(math.Pi / float64(Bands) * (float64(n) + 0.5) * float64(k))
		}
	}
	return b
}

// freqRow is one frame's frequency transform: the first dctFreq cosine
// coefficients over its bands.
type freqRow [dctFreq]float64

func (b *dctBasis) freq(fr *Frame) freqRow {
	var row freqRow
	for k := 0; k < dctFreq; k++ {
		var s float64
		for m := 0; m < Bands; m++ {
			s += float64(fr[m]) * b.f[k][m]
		}
		row[k] = s
	}
	return row
}

// describe computes the descriptor of the window starting at frame `at`.
func (b *dctBasis) describe(frames []Frame, at int) Descriptor {
	var rows [WindowFrames]freqRow
	for n := 0; n < WindowFrames; n++ {
		rows[n] = b.freq(&frames[at+n])
	}
	return b.fromRows(rows[:])
}

// fromRows finishes a descriptor from the window's frequency rows: the time
// transform per coefficient, then unit length.
func (b *dctBasis) fromRows(rows []freqRow) Descriptor {
	var d Descriptor
	var norm float64
	for kt := 0; kt < dctTime; kt++ {
		for kf := 0; kf < dctFreq; kf++ {
			var s float64
			for n := 0; n < WindowFrames; n++ {
				s += rows[n][kf] * b.t[kt][n]
			}
			d[kt*dctFreq+kf] = float32(s)
			norm += s * s
		}
	}
	if norm > 0 {
		inv := float32(1 / math.Sqrt(norm))
		for i := range d {
			d[i] *= inv
		}
	}
	return d
}

// descriptorStream produces the descriptor of every frame position in turn,
// recomputing only the newest frame's frequency row each step.
//
// The descriptor is phase-sensitive by construction - a window of the same
// audio starting two frames later sits as far from the first as a window of
// different audio does - so an index of windows every half second cannot be
// queried with windows every half second: two airings of one spot are aligned
// to within +/-5 frames, and only one phase in ten lines up with the index.
// The index therefore stays at one window per half second, and the QUERY runs
// at every frame: whichever phase lines up, lines up. Memory is the index's;
// time is one frequency row per frame.
type descriptorStream struct {
	b      *dctBasis
	frames []Frame
	rows   [WindowFrames]freqRow
	next   int // frame whose row goes in next
}

func newDescriptorStream(b *dctBasis, frames []Frame) *descriptorStream {
	return &descriptorStream{b: b, frames: frames}
}

// at returns the descriptor of the window starting at frame `at`. Positions
// must be requested in non-decreasing order; a jump recomputes the window.
func (s *descriptorStream) at(at int) Descriptor {
	if at+WindowFrames > len(s.frames) {
		return Descriptor{}
	}
	if s.next < at || s.next > at+WindowFrames {
		s.next = at
	}
	for ; s.next < at+WindowFrames; s.next++ {
		s.rows[s.next%WindowFrames] = s.b.freq(&s.frames[s.next])
	}
	// The ring holds frames at..at+WindowFrames-1 keyed by frame%WindowFrames;
	// present them in window order.
	var ordered [WindowFrames]freqRow
	for n := 0; n < WindowFrames; n++ {
		ordered[n] = s.rows[(at+n)%WindowFrames]
	}
	return s.b.fromRows(ordered[:])
}

// distance is the Euclidean distance between two unit descriptors, 0..2.
func distance(a, b *Descriptor) float64 {
	var s float64
	for i := range a {
		d := float64(a[i] - b[i])
		s += d * d
	}
	return math.Sqrt(s)
}

// QDescriptor is a stored descriptor: one signed byte per component. A unit
// vector's components lie in [-1, 1], so the step is 1/127 - a hundredth of
// the seed distance - and a month's descriptors are 170 MB rather than 690.
type QDescriptor [DescriptorDim]int8

const qScale = 127

func quantize(d *Descriptor) QDescriptor {
	var q QDescriptor
	for i, v := range d {
		x := math.Round(float64(v) * qScale)
		if x > 127 {
			x = 127
		}
		if x < -127 {
			x = -127
		}
		q[i] = int8(x)
	}
	return q
}

// distanceQ is distance against a stored descriptor.
func distanceQ(a *Descriptor, b *QDescriptor) float64 {
	var s float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])/qScale
		s += d * d
	}
	return math.Sqrt(s)
}

// lshIndex is the candidate index: per table a sorted list of (hash, window).
type lshIndex struct {
	planes [lshTables][lshBits]Descriptor
	tables [lshTables][]uint64 // hash<<32 | window id, sorted
}

func newLSHIndex(seed int64) *lshIndex {
	rng := rand.New(rand.NewSource(seed))
	ix := &lshIndex{}
	for t := 0; t < lshTables; t++ {
		for b := 0; b < lshBits; b++ {
			for i := range ix.planes[t][b] {
				ix.planes[t][b][i] = float32(rng.NormFloat64())
			}
		}
	}
	return ix
}

func (ix *lshIndex) hash(t int, d *Descriptor) uint32 {
	var h uint32
	for b := 0; b < lshBits; b++ {
		var s float32
		p := &ix.planes[t][b]
		for i := range d {
			s += p[i] * d[i]
		}
		if s >= 0 {
			h |= 1 << uint(b)
		}
	}
	return h
}

// hashInto appends the next window's keys to every table; windows are
// numbered in the order they are added. finish sorts the tables. Building in
// one sorted pass is cheaper than a hash map holding ten million keys, and
// hashing as the descriptor streams by means the float descriptor never has
// to be kept - only its quantised copy is.
func (ix *lshIndex) hashInto(d *Descriptor) {
	w := uint64(len(ix.tables[0]))
	for t := 0; t < lshTables; t++ {
		ix.tables[t] = append(ix.tables[t], uint64(ix.hash(t, d))<<32|w)
	}
}

func (ix *lshIndex) finish() {
	for t := 0; t < lshTables; t++ {
		keys := ix.tables[t]
		sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
	}
}

// candidates returns the indexed windows sharing a bucket with descriptor d in
// any table, sorted and deduplicated, skipping buckets too large to be
// discriminative.
func (ix *lshIndex) candidates(d *Descriptor, out []uint32) []uint32 {
	out = out[:0]
	for t := 0; t < lshTables; t++ {
		h := uint64(ix.hash(t, d)) << 32
		tab := ix.tables[t]
		lo := sort.Search(len(tab), func(i int) bool { return tab[i] >= h })
		hi := sort.Search(len(tab), func(i int) bool { return tab[i] >= h+(1<<32) })
		if hi-lo > maxBucket {
			continue
		}
		for _, k := range tab[lo:hi] {
			out = append(out, uint32(k))
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	// dedupe
	n := 0
	for i, v := range out {
		if i == 0 || v != out[i-1] {
			out[n] = v
			n++
		}
	}
	return out[:n]
}
