package app

import (
	"fmt"
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
func (a *App) DemoAudio() {
	a.sounds.src = demoAudio{start: time.Now()}
	go a.demoRaid()
}

// demoRaid fakes a raid north-east of you every 10 minutes (starting shortly after launch),
// so the raid panel, map ring and overview card can be seen in demo mode.
func (a *App) demoRaid() {
	time.Sleep(12 * time.Second)
	for {
		var px, py float64
		a.mu.Lock()
		if a.team != nil && a.srv != nil {
			for _, m := range a.team.Members {
				if fmt.Sprint(m.SteamID) == a.srv.PlayerID {
					px, py = float64(m.X), float64(m.Y)
				}
			}
		}
		a.mu.Unlock()
		r := a.sounds.raid
		now := time.Now()
		kinds := []string{"c4", "rocket", "c4", "rocket", "satchel", "c4", "rocket"}
		rx, ry := px+190, py+180 // ~260 m to the north-east
		ax, ay := px-190, py-180 // the spot behind, before front/back is settled
		r.mu.Lock()
		if r.ntpl == 0 {
			r.ntpl = 230
		}
		for i, k := range kinds {
			r.blasts = append(r.blasts, raidBlast{At: now.Add(-time.Duration(len(kinds)-1-i) * 8 * time.Second), Kind: k, Dist: "distant",
				Score: 0.62 + 0.04*float64(i%4), Rel: 38 + float64(i%3)*3, HavePose: true, PX: px, PY: py, Yaw: 8,
				Cand: [2][2]float64{{rx, ry}, {ax, ay}}})
		}
		r.cur = &raidState{Start: now.Add(-48 * time.Second), LastBlast: now, LastPost: now, Posted: len(kinds),
			X: rx, Y: ry, AltX: ax, AltY: ay, Ambiguous: true, Located: true, Count: len(kinds)}
		r.mu.Unlock()
		time.Sleep(10 * time.Minute)
	}
}
