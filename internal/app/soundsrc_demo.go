package app

import (
	"math"
	"math/rand"
	"time"

	"rustwatch/internal/sound"
)

// demoAudio plays battlefield noise with a ship horn every 75 seconds, so the Sounds page
// can be tried in demo mode.
type demoAudio struct{ start time.Time }

func (demoAudio) Ready() (bool, string) { return true, "" }

func (d demoAudio) Run(stop <-chan struct{}, feed func(l, r []float32), started func(string)) error {
	started("demo")
	r := rand.New(rand.NewSource(1))
	horn := sound.SynthHorn(2.8)
	var b float64
	pos := 0
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return nil
		case <-t.C:
		}
		chunk := make([]float32, sound.Rate/10)
		for i := range chunk {
			b = 0.98*b + 0.02*(r.Float64()*2-1)
			v := b * 1.5
			cyc := pos % (75 * sound.Rate)
			if cyc >= 10*sound.Rate && cyc-10*sound.Rate < len(horn) {
				v += float64(horn[cyc-10*sound.Rate]) * 0.6
			}
			chunk[i] = float32(math.Max(-1, math.Min(1, v)))
			pos++
		}
		feed(chunk, chunk)
	}
}

// DemoAudio makes sound detection listen to simulated game audio.
func (a *App) DemoAudio() { a.sounds.src = demoAudio{start: time.Now()} }
