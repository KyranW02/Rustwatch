package sound

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/rand"
	"testing"
)

// synthetic stand-ins for game cues
func horn(sec float64) []float32 { // ship horn: low harmonic drone with slight vibrato, two blasts
	n := int(sec * Rate)
	x := make([]float32, n)
	for i := range x {
		t := float64(i) / Rate
		env := 0.0
		if (t > 0.1 && t < 1.2) || (t > 1.5 && t < 2.6) {
			env = 1
		}
		f := 110 * (1 + 0.004*math.Sin(2*math.Pi*5*t))
		var s float64
		for h, a := range []float64{1, 0.7, 0.5, 0.35, 0.25, 0.15} {
			s += a * math.Sin(2*math.Pi*f*float64(h+1)*t)
		}
		x[i] = float32(0.2 * env * s)
	}
	return x
}

func alarm(sec float64) []float32 { // two-tone siren
	n := int(sec * Rate)
	x := make([]float32, n)
	for i := range x {
		t := float64(i) / Rate
		f := 900.0
		if int(t*3)%2 == 1 {
			f = 1250
		}
		x[i] = float32(0.3 * math.Sin(2*math.Pi*f*t))
	}
	return x
}

func chirps(sec float64) []float32 { // rising sweeps, like a vendor horn melody
	n := int(sec * Rate)
	x := make([]float32, n)
	for i := range x {
		t := float64(i) / Rate
		p := math.Mod(t, 0.6)
		f := 400 + 1500*p
		env := 0.0
		if p < 0.45 {
			env = 1
		}
		x[i] = float32(0.3 * env * math.Sin(2*math.Pi*f*p))
	}
	return x
}

// battlefield noise: brown-ish noise, gunshot bursts and footstep thumps
func noise(sec float64, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	n := int(sec * Rate)
	x := make([]float32, n)
	var b float64
	for i := range x {
		b = 0.98*b + 0.02*(r.Float64()*2-1)
		x[i] = float32(b * 2)
		if r.Intn(Rate/2) == 0 { // shot
			for k := 0; k < Rate/20 && i+k < n; k++ {
				x[i+k] += float32((r.Float64()*2 - 1) * 0.6 * math.Exp(-float64(k)/300))
			}
		}
	}
	return x
}

func mix(base []float32, add []float32, at int, gain float32) []float32 {
	out := append([]float32(nil), base...)
	for i, v := range add {
		if at+i < len(out) {
			out[at+i] += v * gain
		}
	}
	return out
}

func refsFor() (*Matcher, []string) {
	ids := []string{"horn", "alarm", "chirps"}
	return NewMatcher([]Ref{{"horn", Analyze(horn(2.8))}, {"alarm", Analyze(alarm(3))}, {"chirps", Analyze(chirps(3))}}), ids
}

func TestDetectsCueInNoise(t *testing.T) {
	m, ids := refsFor()
	for _, ref := range m.Refs {
		if ref.FP.Peaks < 50 {
			t.Fatalf("%s: only %d peaks", ref.ID, ref.FP.Peaks)
		}
	}
	cues := map[string][]float32{"horn": horn(2.8), "alarm": alarm(3), "chirps": chirps(3)}
	for _, id := range ids {
		for _, gain := range []float32{1, 0.35, 0.15} { // down to a quiet, distant cue
			bg := noise(8, 7)
			at := int(2.3 * Rate)
			live := mix(bg, cues[id], at, gain)
			sc := m.Match(Analyze(live))
			t.Logf("%s gain %.2f: best %s ratio %.2f z %.1f excess %.2f", id, gain, m.Refs[sc[0].Ref].ID, sc[0].Ratio, sc[0].Z, sc[0].Excess)
			if len(sc) == 0 || m.Refs[sc[0].Ref].ID != id || !sc[0].Detected() {
				t.Errorf("%s at gain %.2f: %+v", id, gain, sc)
				continue
			}
			wantOff := float64(at) / hop
			if d := math.Abs(float64(sc[0].Offset) - wantOff); d > 3 && id != "chirps" && id != "alarm" { // repeating cues match one period off too
				t.Errorf("%s offset %d, want ~%.0f", id, sc[0].Offset, wantOff)
			}
			for _, s := range sc[1:] {
				if s.Detected() {
					t.Errorf("%s also triggered %s: %+v", id, m.Refs[s.Ref].ID, s)
				}
			}
		}
	}
}

func TestNoFalseAlarms(t *testing.T) {
	m, _ := refsFor()
	for seed := int64(1); seed <= 6; seed++ {
		live := noise(10, seed)
		// other sounds that aren't cues: a different drone and a beep
		other := make([]float32, 3*Rate)
		for i := range other {
			tt := float64(i) / Rate
			other[i] = float32(0.2*math.Sin(2*math.Pi*173*tt) + 0.1*math.Sin(2*math.Pi*2100*tt))
		}
		live = mix(live, other, 3*Rate, 1)
		for _, s := range m.Match(Analyze(live)) {
			t.Logf("seed %d %s ratio %.2f z %.1f excess %.2f", seed, m.Refs[s.Ref].ID, s.Ratio, s.Z, s.Excess)
			if s.Detected() {
				t.Errorf("seed %d: false %s %+v", seed, m.Refs[s.Ref].ID, s)
			}
		}
	}
}

// Cues arrive as 48 kHz stereo from Windows; the resampler must keep them recognisable.
func TestResampledStereo(t *testing.T) {
	m, _ := refsFor()
	src := 48000
	h := horn(2.8)
	bg := noise(6, 3)
	live12 := mix(bg, h, Rate, 0.5)
	// upsample to 48k stereo (zero-order hold + the resampler's low-pass does the rest)
	var st []float32
	for _, v := range live12 {
		for k := 0; k < 4; k++ {
			st = append(st, v, v)
		}
	}
	r := NewResampler(src, 2)
	var mono []float32
	for i := 0; i < len(st); i += 960 * 2 { // 10 ms chunks
		mono = append(mono, r.Write(st[i:min(len(st), i+960*2)])...)
	}
	if d := len(mono) - len(live12); d > 200 || d < -200 {
		t.Fatalf("resampled length %d vs %d", len(mono), len(live12))
	}
	sc := m.Match(Analyze(mono))
	if len(sc) == 0 || m.Refs[sc[0].Ref].ID != "horn" || !sc[0].Detected() {
		t.Fatalf("after resampling: %+v", sc)
	}
}

func TestWAVRoundTrip(t *testing.T) {
	h := horn(1)
	back, err := DecodeWAV(EncodeWAV(h, Rate))
	if err != nil || len(back) != len(h) {
		t.Fatalf("%v %d %d", err, len(back), len(h))
	}
	for i := range h {
		if d := h[i] - back[i]; d > 0.001 || d < -0.001 {
			t.Fatalf("sample %d: %v vs %v", i, h[i], back[i])
		}
	}
}

// 44.1 kHz stereo float WAV (what game-file extractors and Audacity often write) decodes to
// the same sound.
func TestWAVFloatStereo(t *testing.T) {
	h := horn(2.8)
	var b bytes.Buffer
	n := len(h) * 4 // resample crudely to 48k by repeating: 12k*4
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+n*8))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(3), uint16(2), uint32(48000), uint32(48000 * 8), uint16(8), uint16(32)} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(n*8))
	for _, v := range h {
		for k := 0; k < 4; k++ {
			binary.Write(&b, binary.LittleEndian, math.Float32bits(v))
			binary.Write(&b, binary.LittleEndian, math.Float32bits(v))
		}
	}
	x, err := DecodeWAV(b.Bytes())
	if err != nil || len(x) < len(h)-50 || len(x) > len(h)+50 {
		t.Fatal(err, len(x), len(h))
	}
	m := NewMatcher([]Ref{{"horn", Analyze(x)}})
	sc := m.Match(Analyze(mix(noise(8, 2), horn(2.8), 3*Rate, 0.4)))
	if len(sc) == 0 || !sc[0].Detected() {
		t.Fatalf("decoded file doesn't recognise the horn: %+v", sc)
	}
}
