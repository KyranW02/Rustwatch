package app

import (
	"math"
	"time"

	"rustwatch/internal/rustplus"
)

// Team trails (where each teammate has been in the last hour) and death spots (where the team
// died this wipe), both drawn as map layers.

type TrailPoint struct {
	X, Y float32
	T    int64
}

type DeathSpot struct {
	X    float32 `json:"x"`
	Y    float32 `json:"y"`
	T    int64   `json:"t"`
	Name string  `json:"name"`
}

type TrailView struct {
	Name   string       `json:"name"`
	Online bool         `json:"online"`
	Points [][2]float32 `json:"points"`
}

// recordTrailLocked adds a teammate's position when they've moved. Caller holds a.mu.
func (a *App) recordTrailLocked(m rustplus.Member, now time.Time) {
	if !m.Online || !m.Alive {
		return
	}
	tr := a.trails[m.SteamID]
	if n := len(tr); n > 0 {
		last := tr[n-1]
		if math.Hypot(float64(m.X-last.X), float64(m.Y-last.Y)) < 12 {
			return
		}
		if math.Hypot(float64(m.X-last.X), float64(m.Y-last.Y)) > 600 {
			tr = append(tr, TrailPoint{X: float32(math.NaN()), Y: float32(math.NaN()), T: now.Unix()}) // respawn or teleport: break the line
		}
	}
	tr = append(tr, TrailPoint{m.X, m.Y, now.Unix()})
	cut := now.Add(-2 * time.Hour).Unix()
	i := 0
	for i < len(tr) && (tr[i].T < cut || len(tr)-i > 600) {
		i++
	}
	a.trails[m.SteamID] = tr[i:]
}

func (a *App) recordDeathLocked(m rustplus.Member, x, y float32, now time.Time) {
	a.srv.DeathSpots = append(a.srv.DeathSpots, DeathSpot{X: x, Y: y, T: now.Unix(), Name: m.Name})
	if len(a.srv.DeathSpots) > 300 {
		a.srv.DeathSpots = a.srv.DeathSpots[len(a.srv.DeathSpots)-300:]
	}
	// a death breaks the trail
	a.trails[m.SteamID] = append(a.trails[m.SteamID], TrailPoint{X: float32(math.NaN()), Y: float32(math.NaN()), T: now.Unix()})
}

func (a *App) trailsViewLocked() []TrailView {
	out := []TrailView{}
	if a.team == nil {
		return out
	}
	cut := time.Now().Add(-time.Hour).Unix()
	for _, m := range a.team.Members {
		tv := TrailView{Name: m.Name, Online: m.Online, Points: [][2]float32{}}
		for _, p := range a.trails[m.SteamID] {
			if p.T < cut {
				continue
			}
			if math.IsNaN(float64(p.X)) {
				tv.Points = append(tv.Points, [2]float32{-1, -1}) // gap
				continue
			}
			tv.Points = append(tv.Points, [2]float32{p.X, p.Y})
		}
		if len(tv.Points) > 1 {
			out = append(out, tv)
		}
	}
	return out
}
