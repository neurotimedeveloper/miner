// Package miner finds the segments of a broadcast that air more than once,
// groups their airings into clusters, and says which clusters are spots.
//
// The method is deliberately not a fingerprint. Every 50 ms of audio is kept
// as a 32-band log-mel frame with its gain removed; a repeat is found by
// looking up one-second windows of those frames in a locality-sensitive index,
// and then CONFIRMED and BOUNDED by correlating the two stretches frame by
// frame and growing the match in both directions until the audio stops
// agreeing. That last step is what a fingerprint pipeline lacks, and it is
// where the boundaries of a spot - and the decision that two airings are the
// same spot - actually come from.
package miner

import "time"

// Input is one broadcast file and the broadcast time of its first sample.
type Input struct {
	Path  string
	Start time.Time
}

// Options configures one Mine call. Zero values take the defaults.
type Options struct {
	Inputs []Input

	// TempDir is scratch for decodes and must be set; OutDir receives the
	// representatives and the result.
	TempDir string
	OutDir  string

	FFmpegTimeoutSec int
	MaxConcurrent    int

	// MinRepeatSec is the shortest stretch that counts as a repeat. Below it a
	// match is a coincidence of two similar seconds, not the same audio.
	MinRepeatSec float64
	// MaxRepeatSec caps how far a match is grown. A song played twice is a
	// repeat too; this keeps it from swallowing the block around it.
	MaxRepeatSec float64
	// MinLagSec is the shortest distance between two stretches for a match
	// between them to count. Music repeats itself INSIDE a track - a chorus
	// every minute or so - and every such repeat is a genuine one, just not an
	// airing. On a real day of a music station, 448 of 557 "spots" were two
	// airings less than five minutes apart. A spot's airings are linked through
	// the ones hours apart regardless.
	MinLagSec float64

	// Seed fixes the locality-sensitive index. The method is stochastic only
	// here, and only in the sense that the hyperplanes are drawn once; the same
	// seed gives the same index and the same clusters.
	Seed int64

	// Thresholds. See docs/DESIGN.md, decision 3.
	SeedDistance  float64 // max descriptor distance for a window pair to be worth verifying
	SimHigh       float64 // per-frame cosine at which two frames agree
	SimLow        float64 // per-frame cosine below which they disagree
	MaxDipSec     float64 // how long agreement may lapse inside a repeat
	UnionCoverage float64 // share of both occurrences a match must cover to unite them

	// Representatives controls whether audio samples are written for clusters.
	Representatives bool

	// Progress, when set, is called as the run advances: a month of audio is
	// tens of minutes, and a run that says nothing for that long looks hung.
	Progress func(stage string, done, total int)
	// Debug, when set, receives every confirmed match. Diagnostics only.
	Debug func(m match)
}

func (o Options) progress(stage string, done, total int) {
	if o.Progress != nil {
		o.Progress(stage, done, total)
	}
}

// Occurrence is one airing of a cluster's segment.
type Occurrence struct {
	Start time.Time
	End   time.Time
	// File and OffsetSec locate the airing in the input it came from.
	File      string
	OffsetSec float64
}

// DurationSec is the airing's length.
func (o Occurrence) DurationSec() float64 { return o.End.Sub(o.Start).Seconds() }

// Cluster is every airing of one repeating segment.
type Cluster struct {
	ID          int
	DurationSec float64 // the segment's length, from the consensus of its airings
	Occurrences []Occurrence

	// Representative is the path of the audio sample written for this cluster,
	// or empty when none was requested.
	Representative string

	// IsSpot is the verdict; Reason says why in one line.
	IsSpot bool
	Reason string
}

// SkippedFile is an input that could not be processed. It never fails the run.
type SkippedFile struct {
	Path   string
	Reason string
}

// Stats is what the run measured about itself.
type Stats struct {
	Files         int
	AudioSec      float64
	Frames        int
	Windows       int
	SeedPairs     int
	VerifiedPairs int
	Matches       int
	PeakRSSBytes  int64
	FeatureBytes  int64
	IndexBytes    int64
	WallSec       float64
}

// Result is what Mine returns whenever anything at all was processed.
type Result struct {
	Clusters []Cluster
	Skipped  []SkippedFile
	Warnings []string
	Stats    Stats
}
