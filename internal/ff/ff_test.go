package ff

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping ffmpeg-backed test in -short mode")
	}
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
}

func gen(t *testing.T, path string, durSec float64) string {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "aevalsrc=exprs=0.5*sin(2*PI*440*t):s=22050:d="+strings.TrimRight(strings.TrimRight(
			string(rune('0'+int(durSec)))+".0", "0"), "."),
		"-c:a", "libmp3lame", "-b:a", "64k", path).CombinedOutput()
	if err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	return path
}

// A decode delivers exactly the frames the file holds, at the rate asked for.
func TestStreamPCMDeliversTheWholeFile(t *testing.T) {
	requireFFmpeg(t)
	p := gen(t, filepath.Join(t.TempDir(), "a.mp3"), 5)
	r := NewRunner("ffmpeg", "ffprobe", time.Minute, 2)
	s, err := r.StreamPCM(context.Background(), p, 8000)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, s)
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	// mp3 pads to its frame; five seconds is at least 40 000 frames.
	if frames := n / 2; frames < 5*8000 || frames > 5*8000+2000 {
		t.Errorf("decoded %d frames for 5 s at 8 kHz", frames)
	}
}

// A file that never delivers must die by the timeout, with its whole process
// group, and be reported as a timeout - a recorder whose upstream died looks
// exactly like this.
func TestAHangingDecodeIsKilledByTheTimeout(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	fifo := filepath.Join(dir, "hang.mp3")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("no fifo here: %v", err)
	}
	r := NewRunner("ffmpeg", "ffprobe", 2*time.Second, 2)
	started := time.Now()
	_, err := r.ProbeAudio(context.Background(), fifo)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if time.Since(started) > 15*time.Second {
		t.Errorf("took %s", time.Since(started))
	}
	time.Sleep(300 * time.Millisecond)
	out, _ := exec.Command("ps", "-eo", "args").Output()
	if strings.Contains(string(out), dir) {
		t.Errorf("an ffmpeg/ffprobe survived the kill")
	}
}

func TestExtractWritesTheAskedForStretch(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	p := gen(t, filepath.Join(dir, "a.mp3"), 9)
	r := NewRunner("ffmpeg", "ffprobe", time.Minute, 2)
	dst := filepath.Join(dir, "rep.flac")
	if err := r.Extract(context.Background(), p, 2, 5, dst); err != nil {
		t.Fatal(err)
	}
	s, err := r.StreamPCM(context.Background(), dst, 8000)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, s)
	_ = s.Wait()
	if frames := n / 2; frames < 3*8000-800 || frames > 3*8000+800 {
		t.Errorf("representative holds %d frames, want ~%d", frames, 3*8000)
	}
}
