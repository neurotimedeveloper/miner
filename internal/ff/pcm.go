package ff

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"time"
)

// AudioInfo describes a source file's first audio stream. Length is not here on
// purpose: a trustworthy length requires decoding.
type AudioInfo struct {
	Path       string
	Codec      string
	SampleRate int
	Channels   int
}

type probeJSON struct {
	Streams []struct {
		CodecName  string `json:"codec_name"`
		CodecType  string `json:"codec_type"`
		SampleRate string `json:"sample_rate"`
		Channels   int    `json:"channels"`
	} `json:"streams"`
}

// ProbeAudio returns the parameters of the first audio stream in path, and
// fails when there is no decodable audio stream at all - which is how a broken
// file is recognised before any decode is attempted.
func (r *Runner) ProbeAudio(ctx context.Context, path string) (AudioInfo, error) {
	out, err := r.FFprobeOut(ctx, "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name,codec_type,sample_rate,channels", "-of", "json", path)
	if err != nil {
		return AudioInfo{}, fmt.Errorf("probe %s: %w", path, err)
	}
	var pj probeJSON
	if err := json.Unmarshal(out, &pj); err != nil {
		return AudioInfo{}, fmt.Errorf("probe %s: bad ffprobe json: %w", path, err)
	}
	for _, s := range pj.Streams {
		if s.CodecType != "" && s.CodecType != "audio" {
			continue
		}
		rate, err := strconv.Atoi(s.SampleRate)
		if err != nil || rate <= 0 {
			return AudioInfo{}, fmt.Errorf("probe %s: unusable sample_rate %q", path, s.SampleRate)
		}
		ch := s.Channels
		if ch <= 0 {
			ch = 1
		}
		return AudioInfo{Path: path, Codec: s.CodecName, SampleRate: rate, Channels: ch}, nil
	}
	return AudioInfo{}, fmt.Errorf("probe %s: no audio stream", path)
}

// PCMStream is a running decode whose raw samples can be read incrementally.
// Read to EOF, then Wait: a killed decode looks exactly like a short file at
// the byte level, and only the exit status tells them apart.
type PCMStream struct {
	io.Reader
	cmd     *exec.Cmd
	pipe    io.ReadCloser
	stderr  *bytes.Buffer
	ctx     context.Context
	timeout time.Duration
	cancel  context.CancelFunc
	release func()
	done    bool
	err     error
}

// Wait completes the decode and reports ffmpeg's exit status, saying whether a
// kill was a timeout or a cancellation - the two call for different responses.
func (s *PCMStream) Wait() error {
	if s.done {
		return s.err
	}
	s.done = true
	if err := s.cmd.Wait(); err != nil {
		switch s.ctx.Err() {
		case context.DeadlineExceeded:
			s.err = fmt.Errorf("ff: decode timed out after %s: %w", s.timeout, context.DeadlineExceeded)
		case context.Canceled:
			s.err = fmt.Errorf("ff: decode cancelled: %w", context.Canceled)
		default:
			s.err = fmt.Errorf("ff: decode failed: %w: %s", err, trimErr(s.stderr.String()))
		}
	}
	s.cancel()
	s.release()
	return s.err
}

// Close tears the decode down and releases the concurrency slot.
func (s *PCMStream) Close() error {
	if s.done {
		return s.err
	}
	s.done = true
	_ = s.pipe.Close()
	s.cancel()
	_ = s.cmd.Wait()
	s.release()
	return nil
}

// StreamPCM decodes path to raw mono signed-16-bit little-endian PCM at rate
// and streams it. Mono is enough: repeats are found on a spectral envelope, not
// on the stereo image.
func (r *Runner) StreamPCM(ctx context.Context, path string, rate int) (*PCMStream, error) {
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-r.sem }

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)

	args := append([]string{}, BitexactInput...)
	args = append(args,
		"-i", path, "-map", "0:a:0", "-vn", "-sn", "-dn",
		"-af", fmt.Sprintf("aresample=%d,aformat=channel_layouts=mono", rate),
		"-f", "s16le", "-acodec", "pcm_s16le", "-",
	)
	cmd := exec.CommandContext(ctx, r.FFmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	configureProcessGroup(cmd)

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		release()
		return nil, fmt.Errorf("stream pcm %s: %w", path, err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		release()
		return nil, fmt.Errorf("stream pcm %s: %w", path, err)
	}
	return &PCMStream{Reader: pipe, cmd: cmd, pipe: pipe, stderr: &stderr, ctx: ctx, timeout: timeout, cancel: cancel, release: release}, nil
}

// Extract writes [fromSec, toSec) of path's first audio stream to dst as FLAC,
// which is how a cluster's representative is produced.
func (r *Runner) Extract(ctx context.Context, path string, fromSec, toSec float64, dst string) error {
	args := append([]string{}, BitexactInput...)
	args = append(args,
		"-y", "-ss", fmt.Sprintf("%.3f", fromSec), "-t", fmt.Sprintf("%.3f", toSec-fromSec),
		"-i", path, "-map", "0:a:0", "-vn", "-sn", "-dn",
	)
	args = append(args, BitexactOutput...)
	args = append(args, "-c:a", "flac", "-compression_level", "5", "-f", "flac", dst)
	if err := r.FFmpegRun(ctx, args...); err != nil {
		return fmt.Errorf("extract %s [%.3f,%.3f): %w", path, fromSec, toSec, err)
	}
	return nil
}
