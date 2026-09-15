package miner

import "errors"

var (
	// ErrNothingToMine is returned when no input could be processed.
	ErrNothingToMine = errors.New("miner: nothing to mine")
	// ErrBadOptions covers invalid or missing Options fields.
	ErrBadOptions = errors.New("miner: invalid options")
	// ErrTempDir covers an empty or unusable TempDir.
	ErrTempDir = errors.New("miner: invalid temp dir")
	// ErrOutputIO covers a fatal failure writing results.
	ErrOutputIO = errors.New("miner: output i/o failure")
)
