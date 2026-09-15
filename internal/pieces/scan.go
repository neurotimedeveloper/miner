// Package pieces turns a directory of broadcast files, or an explicit
// manifest, into the Input list the miner consumes.
//
// The miner needs one thing a file cannot carry: the broadcast time of its
// first sample. It comes from the file name or from a manifest, and this
// package is the only place that knows either convention.
package pieces

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/radioenerji/miner"
)

const stampPattern = `^(?P<date>\d{4}-\d{2}-\d{2})[-_T](?P<hh>\d{2})[-:](?P<mm>\d{2})[-:](?P<ss>\d{2})(?:[.,](?P<frac>\d{1,9}))?`

// NamePatterns are tried in order. They cover the recorder's own convention,
// the merger's output, and any name that simply begins with a timestamp.
//
//	2026-09-05-08-00-25.836_araz_fm_srv10-61min.mp3
//	2026-08-30-14-00-00_106fm_merged.flac
//	2026-08-30-14-00-00.mp3
var NamePatterns = []string{
	stampPattern + `_.*$`,
	stampPattern + `\.[A-Za-z0-9]+$`,
}

// AudioExts are the extensions the scanner reads.
var AudioExts = map[string]bool{
	".mp3": true, ".flac": true, ".wav": true, ".m4a": true, ".aac": true,
	".ogg": true, ".opus": true, ".ts": true, ".mp2": true, ".mp4": true, ".mkv": true,
}

// ScanDir resolves every audio file in dir into an input, sorted by start.
// Files whose names carry no timestamp are reported, not skipped silently: a
// file left out is a stretch of broadcast whose repeats will not be found.
func ScanDir(dir, pattern string, loc *time.Location) ([]miner.Input, []error, error) {
	pats := NamePatterns
	if pattern != "" {
		pats = []string{pattern}
	}
	var res []*regexp.Regexp
	for _, p := range pats {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, nil, fmt.Errorf("bad name pattern %q: %w", p, err)
		}
		res = append(res, re)
	}
	if loc == nil {
		loc = time.Local
	}
	var out []miner.Input
	var bad []error
	// Recordings arrive in one directory per day; walk them all.
	err := filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := e.Name()
		if e.IsDir() {
			if path != dir && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || !AudioExts[strings.ToLower(filepath.Ext(name))] {
			return nil
		}
		start, err := parseStart(res, name, loc)
		if err != nil {
			bad = append(bad, fmt.Errorf("%s: %w", name, err))
			return nil
		}
		out = append(out, miner.Input{Path: path, Start: start})
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Start.Before(out[b].Start) })
	return out, bad, nil
}

func parseStart(res []*regexp.Regexp, name string, loc *time.Location) (time.Time, error) {
	for _, re := range res {
		m := re.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		g := map[string]string{}
		for i, n := range re.SubexpNames() {
			if n != "" && i < len(m) {
				g[n] = m[i]
			}
		}
		t, err := time.ParseInLocation("2006-01-02 15:04:05", fmt.Sprintf("%s %s:%s:%s", g["date"], g["hh"], g["mm"], g["ss"]), loc)
		if err != nil {
			return time.Time{}, err
		}
		if frac := g["frac"]; frac != "" {
			for len(frac) < 9 {
				frac += "0"
			}
			ns, _ := strconv.ParseInt(frac[:9], 10, 64)
			t = t.Add(time.Duration(ns))
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("name does not begin with a timestamp (expected e.g. 2026-09-05-08-00-25.836_...)")
}

// Manifest is the explicit input format.
type Manifest struct {
	Files []struct {
		Path  string `json:"path"`
		Start string `json:"start"`
	} `json:"files"`
}

// LoadManifest reads a manifest and resolves relative paths against its own
// directory.
func LoadManifest(path string, loc *time.Location) ([]miner.Input, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var mf Manifest
	if err := json.Unmarshal(raw, &mf); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if loc == nil {
		loc = time.Local
	}
	base := filepath.Dir(path)
	var out []miner.Input
	for i, f := range mf.Files {
		t, err := ParseTime(f.Start, loc)
		if err != nil {
			return nil, fmt.Errorf("manifest file %d (%s): %w", i, f.Path, err)
		}
		p := f.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		out = append(out, miner.Input{Path: p, Start: t})
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Start.Before(out[b].Start) })
	return out, nil
}

// ParseTime accepts RFC3339 and the plain wall-clock forms a human types.
func ParseTime(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02-15-04-05"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable timestamp %q", s)
}
