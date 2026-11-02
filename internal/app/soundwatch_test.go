package app

import (
	"math/rand"
	"strings"
	"testing"

	"rustwatch/internal/sound"
)

func battle(sec float64, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	x := make([]float32, int(sec*sound.Rate))
	var b float64
	for i := range x {
		b = 0.98*b + 0.02*(r.Float64()*2-1)
		x[i] = float32(b * 1.5)
	}
	return x
}

func withCue(bg, cue []float32, at float64, gain float32) []float32 {
	out := append([]float32(nil), bg...)
	s := int(at * sound.Rate)
	for i, v := range cue {
		if s+i < len(out) {
			out[s+i] += v * gain
		}
	}
	return out
}

// Teach a cue from captured audio, then hear it again later.
func TestSoundTeachAndDetect(t *testing.T) {
	a := New(testDir(t))
	a.events = NewEventEngine()
	a.events.Emit = a.alertLocked
	a.events.OnSpawn = a.recordSpawnLocked
	w := a.sounds
	horn := sound.SynthHorn(2.8)

	// 30 s of game audio with the horn 20 s in, as if the player pressed "capture" just after
	w.feed(withCue(battle(30, 1), horn, 20, 0.6))
	id, x, err := w.Capture()
	if err != nil || len(x) != 30*sound.Rate {
		t.Fatal(err, len(x))
	}
	res, err := w.TestCue(id, 19.9, 22.8)
	if err != nil || res["selfOK"] != true {
		t.Fatalf("test %v %v", res, err)
	}
	if _, err := w.SaveCue(id, 19.9, 22.8, "cargo"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.SaveCue(id, 1, 3, "nope"); err == nil {
		t.Fatal("unknown event accepted")
	}
	w.feed(make([]float32, 5*sound.Rate)) // silence
	sid, _, _ := w.Capture()
	if _, err := w.SaveCue(sid, 26, 29, "bradley"); err == nil || !strings.Contains(err.Error(), "too quiet") {
		t.Fatalf("silence accepted as a cue: %v", err)
	}
	v := w.View()
	if len(v.Cues) != 1 || v.Cues[0].Event != "cargo" {
		t.Fatalf("cues %+v", v.Cues)
	}

	// later: different background, quieter horn
	w.mu.Lock()
	m := w.matcher
	w.mu.Unlock()
	if got := w.check(m, battle(8, 9)); len(got) != 0 {
		t.Fatalf("false detection %v", got)
	}
	for _, sc := range m.Match(sound.Analyze(withCue(battle(8, 5), horn, 3, 0.3))) {
		t.Logf("score %+v", sc)
	}
	if got := w.check(m, withCue(battle(8, 5), horn, 3, 0.3)); len(got) != 1 || got[0] != "cargo" {
		t.Fatalf("missed the horn: %v", got)
	}
	if got := w.check(m, withCue(battle(8, 6), horn, 3, 0.3)); len(got) != 0 {
		t.Fatalf("cooldown ignored: %v", got)
	}
	a.mu.Lock()
	heard := a.events.CargoHeard
	sum := a.events.Summary()
	logs := len(a.logs)
	a.mu.Unlock()
	if heard.IsZero() || !strings.Contains(sum.Cargo, "probably still out") || logs == 0 {
		t.Fatalf("event not recorded: %q logs=%d", sum.Cargo, logs)
	}

	// a saved cue survives a restart
	b := New(a.dir)
	if len(b.sounds.View().Cues) != 1 {
		t.Fatal("cue not reloaded")
	}
	if err := b.sounds.DeleteCue(v.Cues[0].ID); err != nil || len(b.sounds.View().Cues) != 0 {
		t.Fatal("delete", err)
	}
	if err := b.sounds.DeleteCue("../config"); err == nil {
		t.Fatal("path traversal")
	}
}
