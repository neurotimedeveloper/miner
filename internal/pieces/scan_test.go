package pieces

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanUnderstandsTheRecorderAndTheMergerNames(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{
		"2026-09-05-08-00-25.836_araz_fm_srv10-61min.mp3",
		"2026-08-30-14-00-00_106fm_merged.flac",
		"2026-08-30-15-00-00.mp3",
		"notes.txt",
		"unstamped.mp3",
	} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, bad, err := ScanDir(dir, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("scanned %d inputs, want 3: %+v", len(got), got)
	}
	if len(bad) != 1 {
		t.Fatalf("reported %d problems, want the unstamped file alone: %v", len(bad), bad)
	}
	// Sorted by start, sub-second kept.
	if got[0].Start.Hour() != 14 || got[1].Start.Hour() != 15 || got[2].Start.Nanosecond() != 836000000 {
		t.Errorf("order or precision wrong: %+v", got)
	}
}
