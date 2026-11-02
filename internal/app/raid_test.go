package app

import (
	"math"
	"strings"
	"testing"
	"time"

	"rustwatch/internal/rustplus"
)

// blastFrom builds what the detector would report for an explosion at (tx, ty) heard by a
// player at (px, py) facing yaw: stereo only gives the left/right angle (front and back fold).
func blastFrom(r *RaidWatch, at time.Time, px, py, yaw, tx, ty float64, dist string) raidBlast {
	bear := math.Atan2(tx-px, ty-py) * 180 / math.Pi
	rel := math.Mod(bear-yaw+540, 360) - 180 // −180…180, + = right
	if rel > 90 {
		rel = 180 - rel
	} else if rel < -90 {
		rel = -180 - rel
	}
	rb := raidBlast{At: at, Kind: "c4", Dist: dist, Score: 0.9, Rel: rel, HavePose: true, PX: px, PY: py, Yaw: yaw}
	d := raidDistances[dist]
	for i, b := range []float64{yaw + rel, yaw + 180 - rel} {
		rad := b * math.Pi / 180
		rb.Cand[i] = [2]float64{px + d*math.Sin(rad), py + d*math.Cos(rad)}
	}
	return rb
}

func TestRaidLocate(t *testing.T) {
	r := &RaidWatch{}
	now := time.Now()
	px, py := 1000.0, 1000.0
	tx, ty := px+130, py+225 // ~260 m to the NNE (its mirror behind is ~450 m away)
	// facing north the whole time: front/back can't be told apart
	var still []raidBlast
	for i := 0; i < 5; i++ {
		still = append(still, blastFrom(r, now, px, py, 0, tx, ty, "distant"))
	}
	x, y, sup, _, _, alt := locate(still)
	if sup != 5 || alt < 4 {
		t.Fatalf("facing one way should be ambiguous: sup %d alt %d", sup, alt)
	}
	_ = x
	_ = y
	// turning between blasts settles it
	var turning []raidBlast
	for i, yaw := range []float64{0, 70, 140, 210, 300} {
		turning = append(turning, blastFrom(r, now.Add(time.Duration(i)*10*time.Second), px, py, yaw, tx, ty, "distant"))
	}
	x, y, sup, _, _, alt = locate(turning)
	if d := math.Hypot(x-tx, y-ty); d > 60 || sup != 5 || alt >= 4 {
		t.Fatalf("turning: off by %.0f m, sup %d alt %d", d, sup, alt)
	}
}

func TestRaidAlert(t *testing.T) {
	a := New(testDir(t))
	a.mu.Lock()
	a.info = &rustplus.Info{MapSize: 4000}
	a.mu.Unlock()
	r := &RaidWatch{a: a}
	now := time.Now()
	px, py, tx, ty := 1500.0, 2000.0, 1500.0+180, 2000.0+180 // NE
	add := func(sec int, yaw float64) []string {
		r.blasts = append(r.blasts, blastFrom(r, now.Add(time.Duration(sec)*time.Second), px, py, yaw, tx, ty, "distant"))
		return r.evaluate(now.Add(time.Duration(sec)*time.Second+time.Second), 4000)
	}
	if m := add(0, 0); len(m) != 0 {
		t.Fatal("one blast shouldn't alert", m)
	}
	if m := add(8, 90); len(m) != 0 {
		t.Fatal("two blasts shouldn't alert", m)
	}
	if m := add(15, 200); len(m) != 0 {
		t.Fatal("three blasts shouldn't alert yet", m)
	}
	m := add(22, 280)
	if len(m) != 1 || !strings.Contains(m[0], "Raid heard: 4 explosions (mostly 4 C4)") || !strings.Contains(m[0], "NE of you") ||
		!strings.Contains(m[0], "near "+Grid(float32(tx), float32(ty), 4000)) {
		t.Fatalf("alert: %v", m)
	}
	if m := add(30, 250); len(m) != 0 {
		t.Fatal("too soon for an update", m)
	}
	add(60, 10)
	if m := add(120, 45); len(m) != 1 || !strings.Contains(m[0], "Raid heard: ") {
		t.Fatalf("update: %v", m)
	}
	// quiet for 4+ minutes → "stopped"
	stop := r.evaluate(now.Add(7*time.Minute), 4000)
	if len(stop) != 1 || !strings.Contains(stop[0], "Raid sounds stopped near") {
		t.Fatalf("stop: %v", stop)
	}
	// without Rust+ (no pose): count-based, direction unknown
	r2 := &RaidWatch{a: a}
	for i := 0; i < 5; i++ {
		r2.blasts = append(r2.blasts, raidBlast{At: now.Add(time.Duration(i) * 5 * time.Second), Kind: "rocket", Dist: "close"})
	}
	if m := r2.evaluate(now.Add(26*time.Second), 4000); len(m) != 1 || !strings.Contains(m[0], "Direction unknown") || !strings.Contains(m[0], "5 rockets") {
		t.Fatalf("no pose: %v", m)
	}
}

func TestCompass(t *testing.T) {
	for deg, want := range map[float64]string{0: "N", 44: "NE", 91: "E", 180: "S", 269: "W", 316: "NW", -10: "N"} {
		if got := compass(deg); got != want {
			t.Errorf("%v: %s", deg, got)
		}
	}
}
