// Command miner finds the repeating segments of a broadcast.
//
//	miner -in ./broadcast -out ./out -temp ./scratch
//	miner -manifest files.json -out ./out -temp ./scratch
//
// The directory mode reads each file's broadcast time from its name; the
// manifest mode states it. Results: out/clusters.json, out/report.txt and, unless
// -no-representatives, out/representatives/cluster-NNNN.flac.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/radioenerji/miner"
	"github.com/radioenerji/miner/internal/pieces"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "miner: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	var (
		in, manifest, out, temp, tz, pattern string
		minRepeat, maxRepeat, minLag         float64
		seed                                 int64
		memLimitGB                           float64
		timeout, maxConc                     int
		noReps, asJSON, strict               bool
		dumpMatches                          string
	)
	fs := flag.NewFlagSet("miner", flag.ContinueOnError)
	fs.StringVar(&in, "in", "", "directory of broadcast files (names begin with the broadcast timestamp)")
	fs.StringVar(&manifest, "manifest", "", "JSON manifest {\"files\":[{\"path\":...,\"start\":...}]}")
	fs.StringVar(&out, "out", "", "output directory")
	fs.StringVar(&temp, "temp", "", "scratch directory (required; no implicit temp dir)")
	fs.StringVar(&tz, "tz", "Local", `timezone for naked timestamps: "Local", "UTC" or an IANA name`)
	fs.StringVar(&pattern, "name-pattern", "", "regexp with named groups date/hh/mm/ss[/frac] replacing the built-in name conventions")
	fs.Float64Var(&minRepeat, "min-repeat", miner.DefaultMinRepeatSec, "shortest repeat worth reporting, seconds")
	fs.Float64Var(&maxRepeat, "max-repeat", miner.DefaultMaxRepeatSec, "longest match grown from one seed, seconds")
	fs.Float64Var(&minLag, "min-lag", miner.DefaultMinLagSec, "two stretches closer than this are not two airings (a chorus inside a track), seconds")
	fs.Int64Var(&seed, "seed", miner.DefaultSeed, "seed for the index; the same seed gives the same clusters")
	fs.Float64Var(&memLimitGB, "memory-limit", 6, "soft memory limit for the Go collector, GB (0 = default)")
	fs.IntVar(&timeout, "ffmpeg-timeout", miner.DefaultFFmpegTimeoutSec, "per-ffmpeg-call timeout, seconds")
	fs.IntVar(&maxConc, "max-concurrent", miner.DefaultMaxConcurrent, "parallel ffmpeg processes")
	fs.BoolVar(&noReps, "no-representatives", false, "do not write an audio sample per cluster")
	fs.StringVar(&dumpMatches, "dump-matches", "", "diagnostic: write every confirmed match, as times, to this file")
	fs.BoolVar(&asJSON, "json", false, "print the result as JSON to stdout")
	fs.BoolVar(&strict, "strict", false, "fail if any input file name could not be understood")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if (in == "") == (manifest == "") {
		return fmt.Errorf("give exactly one of -in or -manifest")
	}
	if out == "" || temp == "" {
		return fmt.Errorf("-out and -temp are required")
	}
	loc, err := loadLocation(tz)
	if err != nil {
		return err
	}

	var inputs []miner.Input
	if manifest != "" {
		inputs, err = pieces.LoadManifest(manifest, loc)
		if err != nil {
			return err
		}
	} else {
		var bad []error
		inputs, bad, err = pieces.ScanDir(in, pattern, loc)
		if err != nil {
			return err
		}
		for _, e := range bad {
			fmt.Fprintln(os.Stderr, "skipped: "+e.Error())
		}
		if strict && len(bad) > 0 {
			return fmt.Errorf("%d input file(s) could not be understood and -strict is set", len(bad))
		}
	}
	if len(inputs) == 0 {
		return fmt.Errorf("no inputs")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(os.Stderr, "mining %d files...\n", len(inputs))
	res, err := miner.Mine(ctx, miner.Options{
		Inputs: inputs, TempDir: temp, OutDir: out,
		FFmpegTimeoutSec: timeout, MaxConcurrent: maxConc,
		MinRepeatSec: minRepeat, MaxRepeatSec: maxRepeat, MinLagSec: minLag, Seed: seed,
		Representatives:  !noReps,
		DumpMatches:      dumpMatches,
		MemoryLimitBytes: int64(memLimitGB * 1e9),
		Progress: func(stage string, done, total int) {
			switch stage {
			case "features":
				if done%25 == 0 || done == total {
					fmt.Fprintf(os.Stderr, "  features: %d/%d files\n", done, total)
				}
			case "index":
				fmt.Fprintf(os.Stderr, "  index built: %d windows\n", total)
			case "search":
				fmt.Fprintf(os.Stderr, "  search: %d/%d hours\n", done/(miner.FPS*3600), total/(miner.FPS*3600))
			}
		},
	})
	if err != nil {
		return err
	}

	js, _ := json.MarshalIndent(res, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "clusters.json"), js, 0o644); err != nil {
		return fmt.Errorf("write clusters.json: %w", err)
	}
	rep, err := os.Create(filepath.Join(out, "report.txt"))
	if err != nil {
		return err
	}
	writeReport(rep, res)
	rep.Close()

	if asJSON {
		os.Stdout.Write(js)
		fmt.Println()
	} else {
		writeSummary(os.Stdout, res)
	}
	return nil
}

func loadLocation(name string) (*time.Location, error) {
	switch name {
	case "", "Local":
		return time.Local, nil
	case "UTC":
		return time.UTC, nil
	}
	return time.LoadLocation(name)
}
