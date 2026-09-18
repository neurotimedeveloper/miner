package miner

import (
	"fmt"
	"os"
	"strings"
)

// Defaults applied when a field is left at its zero value.
const (
	DefaultFFmpegTimeoutSec = 900
	DefaultMaxConcurrent    = 4
	DefaultMinRepeatSec     = 3.0
	DefaultMaxRepeatSec     = 300.0
	DefaultMinLagSec        = 300.0
	DefaultSeed             = 1
	DefaultSeedDistance     = 0.45
	DefaultSimHigh          = 0.60
	DefaultSimLow           = 0.40
	DefaultMaxDipSec        = 2.0
	DefaultUnionCoverage    = 0.6
)

func (o *Options) normalize() error {
	if len(o.Inputs) == 0 {
		return fmt.Errorf("%w: no inputs", ErrNothingToMine)
	}
	if strings.TrimSpace(o.TempDir) == "" {
		return fmt.Errorf("%w: TempDir must be set explicitly (no silent os.TempDir() fallback)", ErrTempDir)
	}
	if st, err := os.Stat(o.TempDir); err != nil || !st.IsDir() {
		return fmt.Errorf("%w: %s is not a usable directory", ErrTempDir, o.TempDir)
	}
	if strings.TrimSpace(o.OutDir) == "" {
		return fmt.Errorf("%w: OutDir is required", ErrBadOptions)
	}
	if o.FFmpegTimeoutSec <= 0 {
		o.FFmpegTimeoutSec = DefaultFFmpegTimeoutSec
	}
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = DefaultMaxConcurrent
	}
	if o.MinRepeatSec == 0 {
		o.MinRepeatSec = DefaultMinRepeatSec
	}
	if o.MaxRepeatSec == 0 {
		o.MaxRepeatSec = DefaultMaxRepeatSec
	}
	if o.MinLagSec == 0 {
		o.MinLagSec = DefaultMinLagSec
	}
	if o.MinLagSec < 0 {
		return fmt.Errorf("%w: MinLagSec must not be negative", ErrBadOptions)
	}
	if o.MinRepeatSec <= 0 || o.MaxRepeatSec <= o.MinRepeatSec {
		return fmt.Errorf("%w: need 0 < MinRepeatSec < MaxRepeatSec, got %v and %v", ErrBadOptions, o.MinRepeatSec, o.MaxRepeatSec)
	}
	if o.Seed == 0 {
		o.Seed = DefaultSeed
	}
	if o.SeedDistance == 0 {
		o.SeedDistance = DefaultSeedDistance
	}
	if o.SimHigh == 0 {
		o.SimHigh = DefaultSimHigh
	}
	if o.SimLow == 0 {
		o.SimLow = DefaultSimLow
	}
	if o.MaxDipSec == 0 {
		o.MaxDipSec = DefaultMaxDipSec
	}
	if o.UnionCoverage == 0 {
		o.UnionCoverage = DefaultUnionCoverage
	}
	if !(o.SimLow < o.SimHigh && o.SimHigh <= 1) {
		return fmt.Errorf("%w: need SimLow < SimHigh <= 1, got %v and %v", ErrBadOptions, o.SimLow, o.SimHigh)
	}
	if o.UnionCoverage <= 0 || o.UnionCoverage > 1 {
		return fmt.Errorf("%w: UnionCoverage must be in (0,1], got %v", ErrBadOptions, o.UnionCoverage)
	}
	return nil
}
