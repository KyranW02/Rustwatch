package sound

import (
	"math"
	"sort"
	"strings"
)

// Explosion detection for raid alerts. Explosions are noise bursts, so the landmark
// fingerprints used for event cues don't suit them. Instead: find sudden loud onsets, then
// compare the next ~1 s of coarse spectrum (mel bands, level-normalised) with templates cut
// from the game's own explosion sounds. Rust ships each explosive as close / distant /
// really-distant variants and plays whichever fits the distance, so the best template also
// gives a distance band. Direction comes from the left/right balance at the moment of impact.

const (
	melBands  = 24
	tplFrames = 48 // ~1 s after the onset
	preFrames = 2
)

var melFB = func() [][]float64 {
	mel := func(f float64) float64 { return 2595 * math.Log10(1+f/700) }
	inv := func(m float64) float64 { return 700 * (math.Pow(10, m/2595) - 1) }
	lo, hi := mel(40), mel(5500)
	pts := make([]float64, melBands+2)
	for i := range pts {
		pts[i] = inv(lo+(hi-lo)*float64(i)/float64(melBands+1)) / (float64(Rate) / fftSize)
	}
	fb := make([][]float64, melBands)
	for b := 0; b < melBands; b++ {
		fb[b] = make([]float64, bins)
		for k := 0; k < bins; k++ {
			x := float64(k)
			switch {
			case x >= pts[b] && x <= pts[b+1]:
				fb[b][k] = (x - pts[b]) / math.Max(pts[b+1]-pts[b], 1e-9)
			case x > pts[b+1] && x <= pts[b+2]:
				fb[b][k] = (pts[b+2] - x) / math.Max(pts[b+2]-pts[b+1], 1e-9)
			}
		}
	}
	return fb
}()

// melFrames turns a spectrogram (dB) into log-mel frames and total energy per frame (dB).
func melFrames(spec [][]float32) ([][]float64, []float64) {
	mf := make([][]float64, len(spec))
	tot := make([]float64, len(spec))
	pw := make([]float64, bins)
	for t, fr := range spec {
		var e float64
		for k := minBin; k < maxBin; k++ {
			pw[k] = math.Pow(10, float64(fr[k])/10)
			e += pw[k]
		}
		tot[t] = 10 * math.Log10(e+1e-12)
		row := make([]float64, melBands)
		for b := 0; b < melBands; b++ {
			var s float64
			for k, w := range melFB[b] {
				if w > 0 {
					s += w * pw[k]
				}
			}
			row[b] = 10 * math.Log10(s+1e-12)
		}
		mf[t] = row
	}
	return mf, tot
}

// notRaid are explosion-like sounds that don't mean a base is being raided. Catapults, cannons
// and MLRS do count: since the 2025 siege update they're raid weapons too. Explosive ammo
// sounds too much like ordinary gunfire to count on its own.
var notRaid = map[string]bool{"x_gun": true, "x_grenade": true, "x_flashbang": true, "x_firework": true,
	"x_molotov": true, "x_heli": true, "x_helirocket": true, "x_bradley": true, "x_other": true, "expammo": true,
	"x_music": true, "x_ambient": true}

// IsRaidKind reports whether a template kind counts as raid activity.
func IsRaidKind(kind string) bool { return kind != "" && !notRaid[kind] }

// BlastTemplate is one known explosion sound.
type BlastTemplate struct {
	ID   string
	Kind string // c4, rocket, hv, satchel, expammo, beancan; "x_…" = not a raid explosive
	Dist string // close, distant, far
	f    []float64
}

// shape cuts and normalises tplFrames of mel frames starting at frame o: level-invariant
// (relative to its own loudest cell, floored), then zero-mean / unit-variance.
func shape(mf [][]float64, o int) []float64 {
	if o < 0 || o+tplFrames > len(mf) {
		return nil
	}
	v := make([]float64, 0, tplFrames*melBands)
	mx := -1e9
	for t := o; t < o+tplFrames; t++ {
		for _, x := range mf[t] {
			mx = math.Max(mx, x)
		}
	}
	for t := o; t < o+tplFrames; t++ {
		for _, x := range mf[t] {
			v = append(v, math.Max(x-mx, -45))
		}
	}
	var m, s float64
	for _, x := range v {
		m += x
	}
	m /= float64(len(v))
	for _, x := range v {
		s += (x - m) * (x - m)
	}
	s = math.Sqrt(s/float64(len(v))) + 1e-9
	for i := range v {
		v[i] = (v[i] - m) / s
	}
	return v
}

// NewBlastTemplate builds a template from a clean recording of one explosion.
func NewBlastTemplate(id string, mono []float32) (BlastTemplate, bool) {
	mf, tot := melFrames(Spectrogram(append(make([]float32, fftSize), mono...)))
	peak := 0
	for t := range tot {
		if tot[t] > tot[peak] {
			peak = t
		}
	}
	o := peak
	for o > 0 && tot[o-1] > tot[peak]-12 { // back up to where it starts rising
		o--
	}
	pad := make([][]float64, tplFrames)
	for i := range pad {
		pad[i] = make([]float64, melBands)
		for j := range pad[i] {
			pad[i][j] = -120
		}
	}
	mf = append(mf, pad...)
	f := shape(mf, max(0, o-preFrames))
	if f == nil {
		return BlastTemplate{}, false
	}
	kind, dist, _ := strings.Cut(strings.SplitN(id, "-", 2)[0], ".")
	if dist == "" {
		dist = "close"
	}
	return BlastTemplate{ID: id, Kind: kind, Dist: dist, f: f}, true
}

func corr(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s / float64(len(a))
}

// Blast is one detected explosion.
type Blast struct {
	Sample                int     // where it starts in the analysed audio (samples at Rate)
	Kind                  string  // best template's kind
	Dist                  string  // close / distant / far
	Score                 float64 // correlation with the best template (0–1)
	Pan                   float64 // −1 hard left … +1 hard right
	LevelDB               float64 // loudness above the background
	Bass                  float64 // share of the blast's energy below ~150 Hz
	Rumble                float64 // seconds the bass stays within 20 dB of its peak
	Raid                  bool    // a raid explosive (not a gun, grenade, firework, …)
	RaidScore, OtherScore float64 // best match among raid explosives / everything else
}

// RaidMargin: a raid explosive must match at least this close to the best non-raid sound
// (music drums and other explosions resemble each other; the decisive cues are the bass checks).
var RaidMargin = 0.04

// Rust's raid explosives put 84–90% of their energy in the deep bass and rumble for
// 0.4–2 s; gunshots sit around 40% and ~0.2 s. Both must pass for a blast to count.
var (
	MinBass   = 0.55
	MinRumble = 0.30
)

// BlastDetector finds explosions in stereo audio.
type BlastDetector struct {
	Templates []BlastTemplate
	MinScore  float64
}

// Detect looks for explosion onsets in [from, len-tplFrames frames) of the stereo audio and
// classifies them. from lets a caller skip audio it has already analysed.
func (d *BlastDetector) Detect(l, r []float32, from int) []Blast {
	n := min(len(l), len(r))
	mono := make([]float32, n)
	for i := 0; i < n; i++ {
		mono[i] = (l[i] + r[i]) / 2
	}
	spec := Spectrogram(mono)
	mf, tot := melFrames(spec)
	specL, specR := Spectrogram(l[:n]), Spectrogram(r[:n])
	band := func(sp [][]float32, t int) float64 { // energy 100 Hz–3 kHz
		var e float64
		for k := 9; k < 256 && t < len(sp); k++ {
			e += math.Pow(10, float64(sp[t][k])/10)
		}
		return e
	}
	var out []Blast
	for _, on := range onsets(tot, from/hop) {
		t, o, p, bg := on.t, on.o, on.p, on.bg
		_ = t
		var best, bestR BlastTemplate
		bs, rs, ns := -1.0, -1.0, -1.0 // best overall, best raid kind, best other
		for _, tp := range d.Templates {
			for sh := -2; sh <= 2; sh++ {
				f := shape(mf, o-preFrames+sh)
				if f == nil {
					continue
				}
				c := corr(f, tp.f)
				if c > bs {
					bs, best = c, tp
				}
				if IsRaidKind(tp.Kind) {
					if c > rs {
						rs, bestR = c, tp
					}
				} else if c > ns {
					ns = c
				}
			}
		}
		if bs < d.MinScore {
			continue
		}
		// direction: background-subtracted left/right energy over the attack
		var el, er, bl, br float64
		for k := o; k < o+6; k++ {
			el += band(specL, k)
			er += band(specR, k)
		}
		for k := max(0, o-25); k < o-3; k++ {
			bl += band(specL, k)
			br += band(specR, k)
		}
		nb := float64(max(1, o-3-max(0, o-25)))
		el = math.Max(el-6*bl/nb, 0)
		er = math.Max(er-6*br/nb, 0)
		pan := 0.0
		if el+er > 0 {
			pan = (er - el) / (er + el)
		}
		bass, rumble := bassFeatures(spec, o, p)
		b := Blast{Sample: o * hop, Kind: best.Kind, Dist: best.Dist, Score: bs, Pan: pan,
			LevelDB: tot[p] - bg, Bass: bass, Rumble: rumble, RaidScore: rs, OtherScore: ns}
		if rs >= d.MinScore && rs >= ns-RaidMargin && bass >= MinBass && rumble >= MinRumble {
			b.Raid, b.Kind, b.Dist, b.Score = true, bestR.Kind, bestR.Dist, rs
		}
		out = append(out, b)
	}
	return out
}

// PanToAngle converts a left/right energy balance to an angle off straight ahead
// (degrees, + = right), assuming a constant-power pan law. Stereo can't tell front from back,
// so the source is at this angle or 180° minus it.
func PanToAngle(pan float64) float64 {
	pan = math.Max(-1, math.Min(1, pan))
	p := 2 / math.Pi * math.Asin(pan) // pan position −1…1
	return math.Asin(math.Max(-1, math.Min(1, p))) * 180 / math.Pi
}

// AngleToGains is the inverse model used in tests and demo audio.
func AngleToGains(deg float64) (gl, gr float64) {
	p := math.Sin(deg * math.Pi / 180) // −1 … 1 (behind maps like in front)
	a := (p + 1) * math.Pi / 4
	return math.Cos(a), math.Sin(a)
}

// bassFeatures measures, with the background before the onset subtracted, the share of energy
// below ~150 Hz over the 300 ms after the peak, and how long the 40–300 Hz band stays within
// 20 dB of its peak.
func bassFeatures(spec [][]float32, o, p int) (float64, float64) {
	pw := func(t, k int) float64 { return math.Pow(10, float64(spec[t][k])/10) }
	bgBin := make([]float64, maxBin)
	nb := 0
	for t := max(0, o-25); t < o-3; t++ {
		for k := minBin; k < maxBin; k++ {
			bgBin[k] += pw(t, k)
		}
		nb++
	}
	for k := range bgBin {
		bgBin[k] /= float64(max(1, nb))
	}
	var lf, all float64
	for t := p; t < p+14 && t < len(spec); t++ {
		for k := minBin; k < maxBin; k++ {
			e := math.Max(pw(t, k)-bgBin[k], 0)
			all += e
			if k <= 13 {
				lf += e
			}
		}
	}
	bass := 0.0
	if all > 0 {
		bass = lf / all
	}
	band := func(t int) float64 {
		var e float64
		for k := 4; k <= 26; k++ {
			e += math.Max(pw(t, k)-bgBin[k], 0)
		}
		return 10 * math.Log10(e+1e-12)
	}
	peak, end := -1e9, p
	for t := o; t < o+8 && t < len(spec); t++ {
		peak = math.Max(peak, band(t))
	}
	for t := o; t < len(spec) && t < o+150; t++ {
		if band(t) > peak-20 {
			end = t
		}
	}
	return bass, float64(end-o) * FrameDur
}

type onset struct {
	t, o, p int
	bg      float64
}

// onsets finds sudden loud starts in a total-energy track (dB per frame), from frame from on,
// leaving room for a template after each.
func onsets(tot []float64, from int) []onset {
	var out []onset
	last := -1000
	const bgFrames = 120
	for t := max(bgFrames/4, from); t+tplFrames < len(tot); t++ {
		if t-last < 12 { // ~0.25 s refractory
			continue
		}
		lo := max(0, t-bgFrames) // background: a low percentile of the preceding seconds
		win := append([]float64(nil), tot[lo:max(lo+1, t-3)]...)
		sort.Float64s(win)
		bg := win[len(win)/5]
		rise := tot[t] - math.Max(tot[max(0, t-3)], tot[max(0, t-2)])
		if tot[t]-bg < 12 || rise < 6 {
			continue
		}
		p := t // the local peak of the attack
		for k := t; k < t+4 && k < len(tot); k++ {
			if tot[k] > tot[p] {
				p = k
			}
		}
		o := t
		for o > 0 && tot[o-1] > tot[p]-12 && o > t-3 {
			o--
		}
		last = t
		out = append(out, onset{t, o, p, bg})
	}
	return out
}

// TemplatesFromClip makes a template from every onset in a longer clip (e.g. music with
// many drum hits), up to max.
func TemplatesFromClip(id string, mono []float32, maxN int) []BlastTemplate {
	x := append(make([]float32, sound30()), mono...)
	pad := make([]float32, (tplFrames+4)*hop)
	x = append(x, pad...)
	mf, tot := melFrames(Spectrogram(x))
	kind, dist, _ := strings.Cut(strings.SplitN(id, "-", 2)[0], ".")
	if dist == "" {
		dist = "close"
	}
	var out []BlastTemplate
	for _, on := range onsets(tot, 0) {
		if f := shape(mf, on.o-preFrames); f != nil {
			out = append(out, BlastTemplate{ID: id, Kind: kind, Dist: dist, f: f})
		}
		if len(out) >= maxN {
			break
		}
	}
	return out
}

// sound30 is the quiet lead-in onsets() needs to measure a background.
func sound30() int { return 40 * hop }
