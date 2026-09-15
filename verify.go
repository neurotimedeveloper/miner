package miner

// Verification: turning a candidate pair of windows into a confirmed, bounded
// repeat - or nothing.
//
// The index only says "these two seconds look alike". Whether they are the
// same audio, and how far the sameness extends, is measured here, frame by
// frame along the lag between them. This is the step a fingerprint pipeline
// does not have, and it is where two things come from that the acceptance
// criteria care about most: the boundaries of a spot when the content around
// each airing differs (decision 4), and the decision that two airings are the
// same spot rather than merely similar (decision 3).

// scoreFrames is the length of the sliding window a similarity verdict is
// taken over. One frame of the same audio through two mp3 encodes can
// disagree; half a second of it cannot. Measured: over ten frames the same
// audio scores 0.69 or better at the 5th percentile and different audio 0.24 or
// worse at the 95th, where single frames overlap.
const scoreFrames = FPS / 2

// match is one confirmed repeat: the same audio at frames [AStart, AEnd) and
// at [AStart-Lag, AEnd-Lag).
type match struct {
	AStart, AEnd int
	Lag          int
	// Sim is the mean per-frame cosine over the matched stretch.
	Sim float64
}

func (m match) BStart() int { return m.AStart - m.Lag }
func (m match) BEnd() int   { return m.AEnd - m.Lag }
func (m match) Len() int    { return m.AEnd - m.AStart }

// verifier grows matches on a timeline under the thresholds.
type verifier struct {
	tl       *timeline
	simHigh  float64
	simLow   float64
	maxDip   int // frames of disagreement tolerated inside a repeat
	minLen   int
	maxLen   int
	lagSlack int // frames either side of the index's lag to try
}

// verify takes a candidate pair (window at frame a, window at frame b, b < a)
// and returns the repeat containing both, if there is one.
//
// The lag is refined first: the index quantises time to half a second, so the
// true alignment is within a few frames of a-b. Then the match is grown in
// both directions from the seed. Agreement is judged over a half-second
// window; a lapse shorter than maxDip - a burst of codec noise, a beat of
// silence that one airing compressed differently - does not end the match, but
// the match ends at the last window that AGREED, never at the end of a lapse.
func (v *verifier) verify(a, b int) (match, bool) {
	lag, sim := v.refineLag(a, b)
	if sim < v.simHigh {
		return match{}, false
	}
	start := v.grow(a, lag, -1)
	end := v.grow(a+WindowFrames-1, lag, +1) + 1
	if end-start < v.minLen {
		return match{}, false
	}
	if end-start > v.maxLen {
		// Grown past any spot: clip around the seed rather than swallow the block.
		half := v.maxLen / 2
		start = maxInt(start, a-half)
		end = minInt(end, a+half)
	}
	m := match{AStart: start, AEnd: end, Lag: lag}
	var s float64
	for t := start; t < end; t++ {
		s += v.tl.cosine(t, t-lag)
	}
	m.Sim = s / float64(end-start)
	// A short match has to be a very good one. A long stretch of agreement is
	// its own evidence - different audio does not agree for twenty seconds -
	// but three seconds of it can happen by chance once in a long programme,
	// and the shortest matches are held to a higher mean.
	if end-start < 2*v.minLen && m.Sim < shortMatchSim {
		return match{}, false
	}
	return m, true
}

// shortMatchSim is the mean similarity a match shorter than twice the minimum
// length must reach.
const shortMatchSim = 0.70

// refineLag picks, within lagSlack frames of a-b, the lag whose one-second
// window agrees best.
func (v *verifier) refineLag(a, b int) (int, float64) {
	bestLag, bestSim := a-b, -1.0
	for d := -v.lagSlack; d <= v.lagSlack; d++ {
		lag := a - b + d
		if !v.inRange(a, lag) || !v.inRange(a+WindowFrames-1, lag) {
			continue
		}
		var s float64
		for t := a; t < a+WindowFrames; t++ {
			s += v.tl.cosine(t, t-lag)
		}
		s /= WindowFrames
		if s > bestSim {
			bestSim, bestLag = s, lag
		}
	}
	return bestLag, bestSim
}

// grow walks from frame t in direction dir (+1/-1) while the two streams keep
// agreeing over a trailing half-second window, and returns the last frame at
// which they still did.
func (v *verifier) grow(t, lag, dir int) int {
	last := t
	dip := 0
	// Ring of the last scoreFrames per-frame similarities, seeded from the
	// frames just inside the seed so the first verdicts are not made on one
	// frame.
	var ring [scoreFrames]float64
	var sum float64
	idx := 0
	for k := 0; k < scoreFrames; k++ {
		f := t - dir*k
		s := 0.0
		if v.inRange(f, lag) {
			s = v.tl.cosine(f, f-lag)
		}
		ring[k] = s
		sum += s
	}
	for cur := t + dir; v.inRange(cur, lag); cur += dir {
		// Neither stream may cross a discontinuity in the axis.
		if v.tl.breakBetween(cur-dir, cur) || v.tl.breakBetween(cur-dir-lag, cur-lag) {
			break
		}
		s := v.tl.cosine(cur, cur-lag)
		sum += s - ring[idx]
		ring[idx] = s
		idx = (idx + 1) % scoreFrames
		score := sum / scoreFrames
		switch {
		case score >= v.simHigh:
			last, dip = cur, 0
		default:
			dip++
			if score < v.simLow || dip > v.maxDip {
				return last
			}
		}
	}
	return last
}

func (v *verifier) inRange(t, lag int) bool {
	return t >= 0 && t < len(v.tl.frames) && t-lag >= 0 && t-lag < len(v.tl.frames)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
