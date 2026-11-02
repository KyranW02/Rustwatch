package app

import (
	"strings"
	"testing"
	"time"

	"rustwatch/internal/rustplus"
)

func TestForecasts(t *testing.T) {
	a := New(testDir(t))
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	sc := &ServerConfig{ID: "x", Deaths: map[string]int{}}
	a.cfg.Servers = []*ServerConfig{sc}
	a.srv = sc
	a.events = NewEventEngine()
	a.status = "connected"

	// 1. nothing known yet
	if f := a.forecastLocked("heli", "Patrol heli", now); f.State != "unknown" {
		t.Fatalf("expected unknown, got %+v", f)
	}
	// 2. wiped 30 min ago: first heli window is wipe + 2..4h
	a.info = &rustplus.Info{WipeTime: uint32(now.Add(-30 * time.Minute).Unix())}
	f := a.forecastLocked("heli", "Patrol heli", now)
	if f.State != "upcoming" || f.Short != "in 1h 30m – 3h 30m" || f.Learned {
		t.Fatalf("from wipe: %+v", f)
	}
	// 3. heli seen 2h30m ago -> window open now
	a.recordSpawnLocked("heli", now.Add(-150*time.Minute), false)
	a.info.WipeTime = uint32(now.Add(-10 * time.Hour).Unix())
	f = a.forecastLocked("heli", "Patrol heli", now)
	if f.State != "window" || !strings.Contains(f.Text, "any time") {
		t.Fatalf("window: %+v", f)
	}
	// window-open alert fires once
	a.windowAlerted = nil
	a.checkWindowsLocked()
	n := len(a.logs)
	a.checkWindowsLocked()
	if len(a.logs) != n {
		t.Fatal("window alert repeated")
	}
	// 4. a modded server running heli every ~45 min: learned after 3 sightings
	sc.EventLog["heli"] = nil
	for _, m := range []int{180, 135, 91, 45} {
		a.recordSpawnLocked("heli", now.Add(-time.Duration(m)*time.Minute), false)
	}
	f = a.forecastLocked("heli", "Patrol heli", now)
	if !f.Learned || f.State != "window" || !strings.Contains(f.Source, "Learned from 4 spawns") {
		t.Fatalf("learned: %+v", f)
	}
	// 5. a restart after the last sighting restarts the clock
	a.recordRestartLocked(now.Add(-5 * time.Minute))
	f = a.forecastLocked("heli", "Patrol heli", now)
	if f.State != "upcoming" || !strings.Contains(f.Text, "server restart") {
		t.Fatalf("restart: %+v", f)
	}
	// learning skips intervals that span a restart
	if _, _, k := learnedWindow([]Stamp{{T: 0}, {T: 3600}, {T: 7200}}, []int64{5000}, [2]float64{2, 4}); k != 1 {
		t.Fatalf("restart-spanning interval used: %d", k)
	}
}

func TestEngineReportsSpawns(t *testing.T) {
	e := NewEventEngine()
	e.MapSize = 3500
	var got []string
	e.OnSpawn = func(k string, at time.Time, approx bool) {
		if approx {
			k += "~"
		}
		got = append(got, k)
	}
	e.Update([]rustplus.Marker{{ID: 1, Type: 8, X: 1700, Y: 1700}}) // already out at start
	e.Update([]rustplus.Marker{{ID: 1, Type: 8, X: 1700, Y: 1700}, {ID: 2, Type: 5, X: -200, Y: 1000}, {ID: 3, Type: 9, X: 1000, Y: 1000}})
	if strings.Join(got, ",") != "heli~,cargo,vendor" {
		t.Fatalf("%v", got)
	}
}
