package miner

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// A synthetic broadcast with known ground truth.
//
// The real list of spots arrives later; until then the acceptance criteria are
// measured on a broadcast built here: non-repeating programme, a set of spots
// each aired several times at different gains, with their edges trimmed by
// different amounts and different programme around them, some in blocks, some
// alone; idents that repeat but are not spots; and one corrupt file. The
// numbers this yields are for the method, not for the channel - but a method
// that cannot reach 100 % here has no business being measured on real data.

const synthRate = 22050

// voice is a deterministic speech-like generator: a sequence of short
// "phonemes" - harmonic stacks on a pitch, shaped by two formants at random
// positions, with noise bursts for consonants and the odd pause. Different
// seeds give different voices; the same seed gives the same audio to the
// sample. The variety matters: a generator with too little of it makes
// unrelated stretches look alike over half-second analysis frames in a way
// real speech and music do not.
type voice struct {
	rng *rand.Rand
}

func newVoice(seed int64) *voice { return &voice{rng: rand.New(rand.NewSource(seed))} }

func (v *voice) render(durSec float64) []float64 {
	n := int(durSec * synthRate)
	out := make([]float64, n)
	pos := 0
	for pos < n {
		segLen := int((0.06 + 0.2*v.rng.Float64()) * synthRate)
		kind := v.rng.Float64()
		switch {
		case kind < 0.12: // pause
			pos += segLen
			continue
		case kind < 0.30: // consonant: shaped noise
			// A one-pole filter at a random cutoff gives each burst its own colour.
			cut := 300 + 3000*v.rng.Float64()
			a := math.Exp(-2 * math.Pi * cut / synthRate)
			loud := 0.15 + 0.25*v.rng.Float64()
			var y float64
			for i := 0; i < segLen && pos+i < n; i++ {
				x := v.rng.Float64()*2 - 1
				y = a*y + (1-a)*x
				env := math.Sin(math.Pi * float64(i) / float64(segLen))
				if v.rng.Float64() < 0.5 {
					out[pos+i] = (x - y) * env * loud // high-passed
				} else {
					out[pos+i] = y * env * loud * 3 // low-passed
				}
			}
			pos += segLen
			continue
		}
		// Voiced: harmonics under two formants.
		f0 := 80 + 320*v.rng.Float64()
		f1 := 250 + 700*v.rng.Float64()
		f2 := 900 + 2200*v.rng.Float64()
		bw1, bw2 := 60+120*v.rng.Float64(), 100+250*v.rng.Float64()
		loud := 0.3 + 0.7*v.rng.Float64()
		glide := (v.rng.Float64() - 0.5) * 0.3 // pitch glide over the segment
		const harmonics = 12
		var amps [harmonics]float64
		for h := 0; h < harmonics; h++ {
			f := f0 * float64(h+1)
			amps[h] = 1/(1+math.Pow((f-f1)/bw1, 2)) + 0.6/(1+math.Pow((f-f2)/bw2, 2)) + 0.03/float64(h+1)
		}
		for i := 0; i < segLen && pos+i < n; i++ {
			t := float64(i) / synthRate
			frac := float64(i) / float64(segLen)
			env := math.Sin(math.Pi * frac)
			pitch := f0 * (1 + glide*frac)
			var s float64
			for h := 0; h < harmonics; h++ {
				s += amps[h] * math.Sin(2*math.Pi*pitch*float64(h+1)*t)
			}
			out[pos+i] = s * env * loud * 0.15
		}
		pos += segLen
	}
	return out
}

// jingle is a short tonal ident: a fixed little melody with decaying notes.
func jingle(seed int64, durSec float64) []float64 {
	rng := rand.New(rand.NewSource(seed))
	n := int(durSec * synthRate)
	out := make([]float64, n)
	notes := 3 + rng.Intn(3)
	per := n / notes
	for k := 0; k < notes; k++ {
		f := 330 * math.Pow(2, float64(rng.Intn(12))/12)
		for i := 0; i < per && k*per+i < n; i++ {
			t := float64(i) / synthRate
			out[k*per+i] = 0.5 * math.Exp(-3*t) * (math.Sin(2*math.Pi*f*t) + 0.4*math.Sin(2*math.Pi*2*f*t))
		}
	}
	return out
}

// airing is one placement of a repeat in the synthetic broadcast.
type airing struct {
	Name     string
	Start    float64 // seconds into the broadcast, of the UNTRIMMED segment
	Dur      float64 // full segment length
	TrimHead float64
	TrimTail float64
	GainDB   float64
	IsSpot   bool
}

// Aired returns the interval actually present in the broadcast.
func (a airing) Aired() (float64, float64) { return a.Start + a.TrimHead, a.Start + a.Dur - a.TrimTail }

// synthBroadcast is a generated broadcast and its truth.
type synthBroadcast struct {
	Files   []Input
	Airings []airing
	DurSec  float64
}

// spotDef is a repeat to place.
type spotDef struct {
	Name   string
	Seed   int64
	Dur    float64
	IsSpot bool
}

// buildBroadcast lays the given repeats over programme at random positions -
// sometimes in blocks of two or three back to back - writes it as hour-ish mp3
// files, and returns the truth. The plan is deterministic in seed.
func buildBroadcast(t *testing.T, dir string, seed int64, totalSec float64, fileSec float64, defs []spotDef, airingsPer int, blockShare float64) synthBroadcast {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	var plan []airing
	pending := map[string]int{}
	for _, d := range defs {
		pending[d.Name] = airingsPer
	}
	cursor := 20.0
	for {
		remaining := 0
		for _, n := range pending {
			remaining += n
		}
		if remaining == 0 || cursor > totalSec-90 {
			break
		}
		cursor += 15 + rng.Float64()*40
		block := 1
		if rng.Float64() < blockShare {
			block = 2 + rng.Intn(2)
		}
		for k := 0; k < block; k++ {
			// pick a repeat with airings left, in a deterministic order
			var names []string
			for _, d := range defs {
				if pending[d.Name] > 0 {
					names = append(names, d.Name)
				}
			}
			if len(names) == 0 {
				break
			}
			d := defs[indexOf(defs, names[rng.Intn(len(names))])]
			a := airing{
				Name: d.Name, Start: cursor, Dur: d.Dur, IsSpot: d.IsSpot,
				TrimHead: 0.5 * rng.Float64() * rng.Float64(),
				TrimTail: 0.5 * rng.Float64() * rng.Float64(),
				GainDB:   -8 + 11*rng.Float64(),
			}
			if a.Start+a.Dur > totalSec-5 {
				break
			}
			plan = append(plan, a)
			pending[d.Name]--
			_, to := a.Aired()
			cursor = to + 0.05 // back to back inside a block
		}
	}
	return renderBroadcast(t, dir, seed, totalSec, fileSec, defs, plan)
}

// renderBroadcast writes a broadcast from an explicit plan: programme that
// never repeats itself, with each planned airing pasted over it.
func renderBroadcast(t *testing.T, dir string, seed int64, totalSec float64, fileSec float64, defs []spotDef, plan []airing) synthBroadcast {
	t.Helper()
	total := int(totalSec * synthRate)
	pcm := make([]float64, total)
	copy(pcm, newVoice(seed*7919).render(totalSec))

	audio := map[string][]float64{}
	for _, d := range defs {
		if d.IsSpot {
			audio[d.Name] = newVoice(d.Seed).render(d.Dur)
		} else {
			audio[d.Name] = jingle(d.Seed, d.Dur)
		}
	}
	for i := range plan {
		a := &plan[i]
		d := defs[indexOf(defs, a.Name)]
		a.Dur, a.IsSpot = d.Dur, d.IsSpot
		from, to := a.Aired()
		gain := math.Pow(10, a.GainDB/20)
		src := audio[d.Name]
		for k := int(from * synthRate); k < int(to*synthRate) && k < total; k++ {
			j := k - int(a.Start*synthRate)
			if j >= 0 && j < len(src) {
				pcm[k] = src[j] * gain
			}
		}
	}
	sort.Slice(plan, func(i, j int) bool { return plan[i].Start < plan[j].Start })

	// Write as consecutive files, encoded as mp3 through ffmpeg so the codec's
	// own effects are in the test.
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	var files []Input
	perFile := int(fileSec * synthRate)
	for k := 0; k*perFile < total; k++ {
		lo, hi := k*perFile, minInt((k+1)*perFile, total)
		wav := filepath.Join(dir, fmt.Sprintf("part%02d.wav", k))
		writeWAV(t, wav, pcm[lo:hi])
		mp3 := filepath.Join(dir, fmt.Sprintf("part%02d.mp3", k))
		if out, err := exec.Command("ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y",
			"-i", wav, "-c:a", "libmp3lame", "-b:a", "64k", mp3).CombinedOutput(); err != nil {
			t.Fatalf("mp3: %v\n%s", err, out)
		}
		os.Remove(wav)
		files = append(files, Input{Path: mp3, Start: base.Add(time.Duration(float64(lo) / synthRate * float64(time.Second)))})
	}
	return synthBroadcast{Files: files, Airings: plan, DurSec: totalSec}
}

func indexOf(defs []spotDef, name string) int {
	for i, d := range defs {
		if d.Name == name {
			return i
		}
	}
	return -1
}

func writeWAV(t *testing.T, path string, pcm []float64) {
	t.Helper()
	data := make([]byte, 44+2*len(pcm))
	copy(data[0:], "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(36+2*len(pcm)))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], synthRate)
	binary.LittleEndian.PutUint32(data[28:], synthRate*2)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(2*len(pcm)))
	for i, v := range pcm {
		if v > 1 {
			v = 1
		}
		if v < -1 {
			v = -1
		}
		binary.LittleEndian.PutUint16(data[44+2*i:], uint16(int16(v*32767)))
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

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

// --- measurement ----------------------------------------------------------

// evaluation is the acceptance criteria measured against the truth.
type evaluation struct {
	Airings       int
	Covered       int // airings that fall inside some cluster's occurrence
	Recall        float64
	ClustersPer   map[string]int // clusters touching each repeat
	Fragmented    int            // repeats with more than one cluster
	SpotFlagRight int
	SpotFlagWrong int
	Extra         int // clusters touching no truth airing
}

// evaluate scores a result against a synthetic broadcast. An airing is covered
// when a cluster occurrence overlaps at least half of what actually aired.
func evaluate(sb synthBroadcast, res Result) evaluation {
	ev := evaluation{ClustersPer: map[string]int{}}
	base := sb.Files[0].Start
	touched := make([]bool, len(res.Clusters))
	perRepeat := map[string]map[int]bool{}
	for _, a := range sb.Airings {
		ev.Airings++
		from, to := a.Aired()
		covered := false
		for ci, c := range res.Clusters {
			for _, o := range c.Occurrences {
				os_, oe := o.Start.Sub(base).Seconds(), o.End.Sub(base).Seconds()
				ov := math.Min(to, oe) - math.Max(from, os_)
				if ov >= 0.5*(to-from) {
					covered = true
					touched[ci] = true
					if perRepeat[a.Name] == nil {
						perRepeat[a.Name] = map[int]bool{}
					}
					perRepeat[a.Name][ci] = true
					if c.IsSpot == a.IsSpot {
						ev.SpotFlagRight++
					} else {
						ev.SpotFlagWrong++
					}
				}
			}
		}
		if covered {
			ev.Covered++
		}
	}
	if ev.Airings > 0 {
		ev.Recall = float64(ev.Covered) / float64(ev.Airings)
	}
	for name, set := range perRepeat {
		ev.ClustersPer[name] = len(set)
		if len(set) > 1 {
			ev.Fragmented++
		}
	}
	for _, x := range touched {
		if !x {
			ev.Extra++
		}
	}
	return ev
}

func (ev evaluation) String() string {
	return fmt.Sprintf("recall %d/%d = %.1f%%, fragmented repeats %d, flag right/wrong %d/%d, clusters outside truth %d",
		ev.Covered, ev.Airings, ev.Recall*100, ev.Fragmented, ev.SpotFlagRight, ev.SpotFlagWrong, ev.Extra)
}
