// Package sound recognises short game sounds (cargo horn, oil rig call-in, …) in a live audio
// stream using landmark fingerprints: spectrogram peaks paired into (f1, f2, Δt) hashes, matched
// by counting hashes that agree on one time offset. Robust to volume and to other sounds mixed in.
package sound

import (
	"math"
	"math/cmplx"
)

// Rate is the internal sample rate (mono). Cues live well below 6 kHz.
const Rate = 12000

const (
	fftSize = 1024
	hop     = 256 // ~21 ms per frame
	bins    = fftSize / 2
	minBin  = 4   // ~47 Hz
	maxBin  = 470 // ~5.5 kHz
)

// FrameDur is the time between spectrogram frames.
const FrameDur = float64(hop) / Rate

// Resampler turns interleaved float32 audio at any rate/channel count into mono at Rate,
// with a windowed-sinc low-pass so nothing aliases. It keeps state between Write calls.
type Resampler struct {
	ch   int
	step float64 // input samples per output sample
	cut  float64 // normalised cutoff (1 = input Nyquist)
	half int
	buf  []float32
	t    float64
}

func NewResampler(srcRate, channels int) *Resampler {
	if channels < 1 {
		channels = 1
	}
	step := float64(srcRate) / Rate
	cut := 1.0
	if step > 1 {
		cut = 1 / step
	}
	cut *= 0.92
	half := int(math.Ceil(8 / cut))
	return &Resampler{ch: channels, step: step, cut: cut, half: half, t: float64(half)}
}

func (r *Resampler) kernel(x float64) float64 {
	ax := math.Abs(x)
	if ax >= float64(r.half) {
		return 0
	}
	w := 0.5 + 0.5*math.Cos(math.Pi*ax/float64(r.half))
	a := math.Pi * r.cut * x
	s := 1.0
	if a != 0 {
		s = math.Sin(a) / a
	}
	return r.cut * s * w
}

// Write accepts interleaved samples and returns the mono output produced so far.
func (r *Resampler) Write(in []float32) []float32 {
	for i := 0; i+r.ch <= len(in); i += r.ch {
		var s float32
		for c := 0; c < r.ch; c++ {
			s += in[i+c]
		}
		r.buf = append(r.buf, s/float32(r.ch))
	}
	var out []float32
	for r.t+float64(r.half) < float64(len(r.buf)) {
		c := int(r.t)
		var acc float64
		for n := c - r.half + 1; n <= c+r.half; n++ {
			if n >= 0 {
				acc += float64(r.buf[n]) * r.kernel(r.t-float64(n))
			}
		}
		out = append(out, float32(acc))
		r.t += r.step
	}
	if drop := int(r.t) - r.half; drop > 0 {
		r.buf = append(r.buf[:0], r.buf[drop:]...)
		r.t -= float64(drop)
	}
	return out
}

func fft(a []complex128) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for l := 2; l <= n; l <<= 1 {
		w := cmplx.Exp(complex(0, -2*math.Pi/float64(l)))
		for i := 0; i < n; i += l {
			wn := complex(1, 0)
			for k := 0; k < l/2; k++ {
				u, v := a[i+k], a[i+k+l/2]*wn
				a[i+k], a[i+k+l/2] = u+v, u-v
				wn *= w
			}
		}
	}
}

var hann = func() []float64 {
	w := make([]float64, fftSize)
	for i := range w {
		w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(fftSize))
	}
	return w
}()

// Spectrogram returns log-magnitude (dB) frames of mono audio at Rate.
func Spectrogram(x []float32) [][]float32 {
	var out [][]float32
	buf := make([]complex128, fftSize)
	for start := 0; start+fftSize <= len(x); start += hop {
		for i := 0; i < fftSize; i++ {
			buf[i] = complex(float64(x[start+i])*hann[i], 0)
		}
		fft(buf)
		fr := make([]float32, bins)
		for k := 0; k < bins; k++ {
			m := cmplx.Abs(buf[k])
			fr[k] = float32(20 * math.Log10(m+1e-9))
		}
		out = append(out, fr)
	}
	return out
}

// RMS is the loudness of a block (for level meters).
func RMS(x []float32) float64 {
	if len(x) == 0 {
		return 0
	}
	var s float64
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(x)))
}

// SynthHorn makes a ship-horn-like test sound (two blasts of a low harmonic drone).
func SynthHorn(sec float64) []float32 {
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
