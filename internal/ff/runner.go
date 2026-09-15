// Package ff wraps every ffmpeg/ffprobe invocation this project makes.
// It is a trimmed copy of merger's package of the same name: the projects
// are separate on purpose, and the two guarantees below are worth having twice.
//
// Two guarantees are centralised here so no call site can forget them:
//
//   - Determinism. Every encode carries -fflags +bitexact -flags:a +bitexact
//     -map_metadata -1. Without them FLAC records ENCODER=Lavf<version>,
//     padding and STREAMINFO fields that drift between ffmpeg builds.
//   - Process hygiene. Commands run in their own process group and the whole
//     group is killed on timeout or cancellation, so a wedged ffmpeg reading a
//     corrupt file cannot leave zombies behind.
package ff

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// BitexactInput are the flags applied to every invocation, encode or not.
var BitexactInput = []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-fflags", "+bitexact"}

// BitexactOutput are the flags applied to every output that gets encoded.
// -map_metadata -1 strips container metadata; +bitexact stops the encoder from
// stamping its own version into the stream.
var BitexactOutput = []string{"-flags:a", "+bitexact", "-map_metadata", "-1", "-map_chapters", "-1"}

// Runner executes ffmpeg/ffprobe with a bounded concurrency and a per-call
// timeout.
type Runner struct {
	FFmpeg  string
	FFprobe string
	Timeout time.Duration

	sem chan struct{}
}

// NewRunner returns a Runner limited to maxConcurrent simultaneous processes.
// The limit is per Merge call; a global per-slot cap is the caller's job.
func NewRunner(ffmpeg, ffprobe string, timeout time.Duration, maxConcurrent int) *Runner {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Runner{
		FFmpeg:  ffmpeg,
		FFprobe: ffprobe,
		Timeout: timeout,
		sem:     make(chan struct{}, maxConcurrent),
	}
}

// Check verifies both binaries are present and runnable before any real work
// starts, so a missing ffmpeg fails fast instead of once per piece.
func (r *Runner) Check(ctx context.Context) error {
	for _, bin := range []string{r.FFmpeg, r.FFprobe} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("ff: %q not found in PATH: %w", bin, err)
		}
	}
	return nil
}

// FFmpegRun runs ffmpeg and returns its combined stderr on failure.
func (r *Runner) FFmpegRun(ctx context.Context, args ...string) error {
	_, err := r.run(ctx, r.FFmpeg, args)
	return err
}

// FFprobeOut runs ffprobe and returns its stdout.
func (r *Runner) FFprobeOut(ctx context.Context, args ...string) ([]byte, error) {
	return r.run(ctx, r.FFprobe, args)
}

// FFmpegOut runs ffmpeg and returns its stdout (used for -f md5 and raw PCM).
func (r *Runner) FFmpegOut(ctx context.Context, args ...string) ([]byte, error) {
	return r.run(ctx, r.FFmpeg, args)
}

func (r *Runner) run(ctx context.Context, bin string, args []string) ([]byte, error) {
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	configureProcessGroup(cmd)

	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return stdout.Bytes(), fmt.Errorf("ff: %s timed out after %s: %w", short(bin), timeout, ctx.Err())
		}
		if ctx.Err() != nil {
			return stdout.Bytes(), fmt.Errorf("ff: %s cancelled: %w", short(bin), ctx.Err())
		}
		return stdout.Bytes(), fmt.Errorf("ff: %s failed: %w: %s", short(bin), err, trimErr(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func short(bin string) string {
	if i := strings.LastIndex(bin, "/"); i >= 0 {
		return bin[i+1:]
	}
	return bin
}

func trimErr(s string) string {
	s = strings.TrimSpace(s)
	const max = 600
	if len(s) > max {
		return "..." + s[len(s)-max:]
	}
	return s
}
