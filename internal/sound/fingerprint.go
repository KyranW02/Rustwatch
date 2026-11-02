package sound

import (
	"math"
	"sort"
)

// A fingerprint is, for each ~21 ms frame, the frequency bins that stand out as spectral
// peaks (a horn's harmonics, an alarm's tones). Peaks depend on the shape of the spectrum, not
// its volume, and other sounds mixed in only add peaks elsewhere. Matching slides the reference
// over the live audio and counts how many of its peaks are present at each time offset.

const (
	peakNb   = 3   // a peak is the max within ±3 bins (~35 Hz) — resolves low harmonics
	minAbsDB = -12 // ignore near-silence (≈ -60 dBFS)
	overDB   = 8   // must stand out from the frame's typical level
	perFrame = 12  // keep the strongest peaks per frame
	words    = (maxBin + 63) / 64
)

type frameBits [words]uint64

func (b *frameBits) set(f int)      { b[f>>6] |= 1 << (f & 63) }
func (b *frameBits) has(f int) bool { return b[f>>6]&(1<<(f&63)) != 0 }

// Fingerprint is the per-frame peak bins of a clip.
type Fingerprint struct {
	Frames [][]uint16
	Peaks  int
}

func framePeaks(fr []float32) []uint16 {
	tmp := append([]float32(nil), fr[minBin:maxBin]...)
	sort.Slice(tmp, func(i, j int) bool { return tmp[i] < tmp[j] })
	floor := tmp[len(tmp)/2]
	type pk struct {
		f int
		v float32
	}
	var ps []pk
	for f := minBin + peakNb; f < maxBin-peakNb; f++ {
		v := fr[f]
		if v < minAbsDB || v < floor+overDB {
			continue
		}
		ok := true
		for g := f - peakNb; g <= f+peakNb; g++ {
			if g != f && fr[g] >= v {
				ok = false
				break
			}
		}
		if ok {
			ps = append(ps, pk{f, v})
		}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].v > ps[j].v })
	if len(ps) > perFrame {
		ps = ps[:perFrame]
	}
	out := make([]uint16, len(ps))
	for i, p := range ps {
		out[i] = uint16(p.f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Analyze fingerprints mono audio at Rate.
func Analyze(x []float32) Fingerprint {
	var fp Fingerprint
	for _, fr := range Spectrogram(x) {
		p := framePeaks(fr)
		fp.Frames = append(fp.Frames, p)
		fp.Peaks += len(p)
	}
	return fp
}

// Trim drops leading/trailing frames without peaks (silence around a saved cue).
func (fp Fingerprint) Trim() Fingerprint {
	a, b := 0, len(fp.Frames)
	for a < b && len(fp.Frames[a]) == 0 {
		a++
	}
	for b > a && len(fp.Frames[b-1]) == 0 {
		b--
	}
	return Fingerprint{Frames: fp.Frames[a:b], Peaks: fp.Peaks}
}

// Stable keeps only peaks that continue into a neighbouring frame (within a bin). A saved cue
// carries the background noise of the moment it was recorded; that noise is random from frame
// to frame and never matches again, while the cue's tones are steady. Sounds that are noise by
// nature (explosions) keep all their peaks.
func (fp Fingerprint) Stable() Fingerprint {
	near := func(fr []uint16, f uint16) bool {
		for _, g := range fr {
			if g+1 >= f && g <= f+1 {
				return true
			}
		}
		return false
	}
	out := Fingerprint{Frames: make([][]uint16, len(fp.Frames))}
	for t, fr := range fp.Frames {
		for _, f := range fr {
			if (t > 0 && near(fp.Frames[t-1], f)) || (t+1 < len(fp.Frames) && near(fp.Frames[t+1], f)) {
				out.Frames[t] = append(out.Frames[t], f)
				out.Peaks++
			}
		}
	}
	if out.Peaks*10 < fp.Peaks*3 {
		return fp
	}
	return out
}

// Ref is a known sound.
type Ref struct {
	ID string
	FP Fingerprint
}

// Matcher finds known sounds in audio.
type Matcher struct{ Refs []Ref }

func NewMatcher(refs []Ref) *Matcher {
	for i := range refs {
		refs[i].FP = refs[i].FP.Stable().Trim()
	}
	return &Matcher{Refs: refs}
}

// Score is how well a reference matched at its best time offset.
type Score struct {
	Ref    int
	Ratio  float64 // share of the reference's peaks found in the live audio
	Z      float64 // how far that stands above chance (other offsets)
	Excess float64 // (Ratio - chance) / (1 - chance), chance from how busy each frequency is
	Offset int     // frame in the analysed audio where the reference starts
}

// Thresholds for a detection, tuned on mixed test audio.
var (
	MinRatio  = 0.30
	MinExcess = 0.25
	MinZ      = 4.0
)

// Detected reports whether a score is a confident match.
func (s Score) Detected() bool { return s.Ratio >= MinRatio && s.Excess >= MinExcess && s.Z >= MinZ }

// Match scores every reference against fingerprinted audio, best first.
func (m *Matcher) Match(live Fingerprint) []Score {
	// live peaks, widened by one bin for small pitch/Doppler differences
	lb := make([]frameBits, len(live.Frames))
	for t, fr := range live.Frames {
		for _, f := range fr {
			for g := int(f) - 1; g <= int(f)+1; g++ {
				if g >= 0 && g < maxBin {
					lb[t].set(g)
				}
			}
		}
	}
	// how often each (widened) bin is lit across the live audio: the chance of a random hit
	occ := make([]float64, maxBin)
	for t := range lb {
		for f := 0; f < maxBin; f++ {
			if lb[t].has(f) {
				occ[f]++
			}
		}
	}
	for f := range occ {
		occ[f] /= float64(max(1, len(lb)))
	}
	var out []Score
	for ri, r := range m.Refs {
		total := 0
		for _, fr := range r.FP.Frames {
			total += len(fr)
		}
		nl := len(lb) - len(r.FP.Frames) + 1
		if total < 20 || nl < 1 {
			continue
		}
		scores := make([]float64, nl)
		for lag := 0; lag < nl; lag++ {
			hit := 0
			for t, fr := range r.FP.Frames {
				b := &lb[lag+t]
				for _, f := range fr {
					if b.has(int(f)) {
						hit++
					}
				}
			}
			scores[lag] = float64(hit) / float64(total)
		}
		best, bl := 0.0, 0
		for l, s := range scores {
			if s > best {
				best, bl = s, l
			}
		}
		// chance level: offsets away from the best one
		var sum, sq float64
		n := 0
		for l, s := range scores {
			if l < bl-len(r.FP.Frames)/2 || l > bl+len(r.FP.Frames)/2 {
				sum += s
				sq += s * s
				n++
			}
		}
		z := 0.0
		if n >= 5 {
			mean := sum / float64(n)
			sd := math.Sqrt(math.Max(sq/float64(n)-mean*mean, 1e-6))
			z = (best - mean) / math.Max(sd, 0.02)
		} else {
			z = best / 0.05 // too little context to estimate chance; be conservative
		}
		var chance float64
		for _, fr := range r.FP.Frames {
			for _, f := range fr {
				chance += occ[f]
			}
		}
		chance /= float64(total)
		excess := 0.0
		if chance < 1 {
			excess = (best - chance) / (1 - chance)
		}
		out = append(out, Score{Ref: ri, Ratio: best, Z: z, Excess: excess, Offset: bl})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Excess > out[j].Excess })
	return out
}
