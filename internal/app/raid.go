package app

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"rustwatch/internal/rustplus"
	"rustwatch/internal/sound"
)

// Raid detection: recognise explosions in Rust's audio (internal/sound/blast.go), work out
// roughly where each came from — left/right balance + the player's facing from Rust+ + which
// distance variant of the sound the game played — and when several agree on a place, post an
// estimated grid to team chat. Stereo can't tell front from back, so each blast gives two
// possible spots; as the player turns between blasts the real one collects the votes.

var raidDistances = map[string]float64{"close": 80, "distant": 260, "far": 550} // metres, rough

var raidKindNames = map[string][2]string{
	"c4": {"C4", "C4"}, "rocket": {"rocket", "rockets"}, "hv": {"HV rocket", "HV rockets"},
	"satchel": {"satchel", "satchels"}, "beancan": {"beancan", "beancans"},
	"x_catapult": {"catapult shot", "catapult shots"}, "x_cannon": {"cannon shot", "cannon shots"},
	"x_mlrs": {"MLRS rocket", "MLRS rockets"},
}

type raidBlast struct {
	At       time.Time
	Kind     string
	Dist     string
	Score    float64
	Rel      float64 // degrees right of where the player faced (or 180−Rel behind)
	HavePose bool
	PX, PY   float64       // player position
	Yaw      float64       // player facing, compass degrees
	Cand     [2][2]float64 // possible positions: in front / behind
}

type raidState struct {
	Start, LastBlast, LastPost time.Time
	Posted                     int // blasts already reported
	X, Y, AltX, AltY           float64
	Ambiguous, Located         bool
	Count                      int
}

// RaidWatch is the raid detector.
type RaidWatch struct {
	a         *App
	mu        sync.Mutex // guards blasts and cur; never held while taking a.mu
	dir       string
	det       *sound.BlastDetector
	ntpl      int
	processed int64
	blasts    []raidBlast
	cur       *raidState
	sig       string
	clock     func(samples int64) time.Time // audio clock: when sample n was heard (tests replace it)
	lastAbs   int64
	lastPan   float64
}

func newRaidWatch(a *App, soundsDir string) *RaidWatch {
	r := &RaidWatch{a: a, dir: filepath.Join(soundsDir, "raid")}
	r.clock = func(int64) time.Time { return time.Now() }
	r.reload()
	return r
}

func (r *RaidWatch) signature() string {
	ents, _ := os.ReadDir(r.dir)
	return fmt.Sprint(len(ents), func() int64 {
		var t int64
		for _, e := range ents {
			if fi, err := e.Info(); err == nil {
				t += fi.ModTime().UnixNano() + fi.Size()
			}
		}
		return t
	}())
}

// reload builds explosion templates from sounds/raid/<kind>.<dist>-<n>.wav files.
func (r *RaidWatch) reload() {
	r.sig = r.signature()
	files, _ := filepath.Glob(filepath.Join(r.dir, "*.wav"))
	var tpls []sound.BlastTemplate
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		x, err := sound.DecodeWAV(b)
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(f), ".wav")
		if strings.HasPrefix(id, "x_music") {
			tpls = append(tpls, sound.TemplatesFromClip(id, x, max(12, 2*len(x)/sound.Rate))...)
		} else if t, ok := sound.NewBlastTemplate(id, x); ok {
			tpls = append(tpls, t)
		}
	}
	r.mu.Lock()
	r.ntpl = len(tpls)
	r.mu.Unlock()
	r.det = nil
	if len(tpls) > 0 {
		r.det = &sound.BlastDetector{Templates: tpls, MinScore: 0.6}
		log.Printf("raid: %d explosion templates loaded", len(tpls))
	}
}

// tick analyses new audio; called about once a second from the sound loop.
func (r *RaidWatch) tick(w *SoundWatch) {
	if sig := r.signature(); sig != r.sig {
		r.reload()
	}
	r.a.mu.Lock()
	off := r.a.cfg.Settings.RaidOff
	r.a.mu.Unlock()
	w.mu.Lock()
	mode := w.mode
	w.mu.Unlock()
	if off || r.det == nil || mode == "" {
		return
	}
	l, rr, total := w.lastStereo(6.5)
	if len(l) < 4*sound.Rate {
		return
	}
	start := total - int64(len(l))
	from := int(max(0, r.processed-start))
	now := r.clock(total)
	var fresh []raidBlast
	for _, b := range r.det.Detect(l, rr, from) {
		abs := start + int64(b.Sample)
		if abs < r.processed || !b.Raid {
			continue
		}
		// one explosion often has follow-up bangs (debris, echoes): same direction, just after
		if abs-r.lastAbs < int64(1.2*sound.Rate) && math.Abs(b.Pan-r.lastPan) < 0.35 {
			continue
		}
		r.lastAbs, r.lastPan = abs, b.Pan
		at := now.Add(-time.Duration(float64(total-abs) / sound.Rate * float64(time.Second)))
		fresh = append(fresh, r.make(b, at))
	}
	// everything up to ~1.3 s before the end has had its full look
	r.processed = max(r.processed, total-int64(1.3*sound.Rate))
	r.a.mu.Lock()
	var size uint32
	if r.a.info != nil {
		size = r.a.info.MapSize
	}
	r.a.mu.Unlock()
	r.mu.Lock()
	r.blasts = append(r.blasts, fresh...)
	cut := now.Add(-10 * time.Minute)
	for len(r.blasts) > 0 && r.blasts[0].At.Before(cut) {
		r.blasts = r.blasts[1:]
	}
	msgs := r.evaluate(now, size)
	r.mu.Unlock()
	for _, m := range msgs {
		r.post(m)
	}
	if len(fresh) > 0 {
		r.a.publish("update", nil)
	}
}

// pose is the player's position and facing from Rust+ (fetched fresh if the last is stale).
func (r *RaidWatch) pose() (x, y, yaw float64, ok bool) {
	a := r.a
	a.mu.Lock()
	c, stale := a.client, time.Since(a.markersAt) > 3*time.Second
	a.mu.Unlock()
	if c != nil && stale {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if mk, _, err := c.GetMapMarkersRaw(ctx); err == nil {
			a.mu.Lock()
			a.markers, a.markersAt = mk, time.Now()
			a.mu.Unlock()
		}
		cancel()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.srv == nil {
		return 0, 0, 0, false
	}
	for _, m := range a.markers {
		if m.Type == rustplus.MarkerPlayer && fmt.Sprint(m.SteamID) == a.srv.PlayerID {
			return float64(m.X), float64(m.Y), float64(m.Rotation), true
		}
	}
	return 0, 0, 0, false
}

// make turns a detected blast into a located one (looks up the player's pose; no locks held).
func (r *RaidWatch) make(b sound.Blast, at time.Time) raidBlast {
	rb := raidBlast{At: at, Kind: b.Kind, Dist: b.Dist, Score: b.Score, Rel: sound.PanToAngle(b.Pan)}
	if x, y, yaw, ok := r.pose(); ok {
		rb.HavePose, rb.PX, rb.PY, rb.Yaw = true, x, y, yaw
		d := raidDistances[b.Dist]
		if d == 0 {
			d = 250
		}
		for i, bear := range []float64{yaw + rb.Rel, yaw + 180 - rb.Rel} {
			rad := bear * math.Pi / 180
			rb.Cand[i] = [2]float64{x + d*math.Sin(rad), y + d*math.Cos(rad)}
		}
	}
	log.Printf("raid: heard %s (%s, match %.2f), %.0f° %s of facing", b.Kind, b.Dist, b.Score, math.Abs(rb.Rel), map[bool]string{true: "right", false: "left"}[rb.Rel >= 0])
	return rb
}

const raidSigma = 150.0 // metres: how far apart blasts can be and still be "the same place"

// locate finds the spot most blasts agree on, and the best different spot (the mirror).
func locate(bs []raidBlast) (x, y float64, support int, ax, ay float64, alt int) {
	score := func(cx, cy float64) (float64, int) {
		var s float64
		n := 0
		for _, b := range bs {
			best := math.Inf(1)
			for _, c := range b.Cand {
				best = math.Min(best, math.Hypot(c[0]-cx, c[1]-cy))
			}
			s += math.Exp(-best * best / (2 * raidSigma * raidSigma))
			if best < 1.5*raidSigma {
				n++
			}
		}
		return s, n
	}
	type pt struct {
		x, y, s float64
		n       int
	}
	var pts []pt
	for _, b := range bs {
		for _, c := range b.Cand {
			s, n := score(c[0], c[1])
			pts = append(pts, pt{c[0], c[1], s, n})
		}
	}
	if len(pts) == 0 {
		return
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].s > pts[j].s })
	best := pts[0]
	// refine: average the nearest candidate of each agreeing blast
	var sx, sy float64
	k := 0
	for _, b := range bs {
		bi, bd := 0, math.Inf(1)
		for i, c := range b.Cand {
			if d := math.Hypot(c[0]-best.x, c[1]-best.y); d < bd {
				bi, bd = i, d
			}
		}
		if bd < 1.5*raidSigma {
			sx += b.Cand[bi][0]
			sy += b.Cand[bi][1]
			k++
		}
	}
	x, y, support = sx/float64(k), sy/float64(k), k
	for _, p := range pts[1:] {
		if math.Hypot(p.x-x, p.y-y) > 2*raidSigma {
			ax, ay, alt = p.x, p.y, p.n
			break
		}
	}
	return
}

// evaluate updates the current raid and returns messages to post. Caller holds r.mu.
func (r *RaidWatch) evaluate(now time.Time, size uint32) (msgs []string) {
	var recent, located []raidBlast
	for _, b := range r.blasts {
		if now.Sub(b.At) <= 2*time.Minute {
			recent = append(recent, b)
			if b.HavePose {
				located = append(located, b)
			}
		}
	}
	lastMin := 0
	for _, b := range recent {
		if now.Sub(b.At) <= time.Minute {
			lastMin++
		}
	}
	cur := r.cur
	// a raid that has gone quiet
	if cur != nil && now.Sub(cur.LastBlast) > 4*time.Minute {
		if cur.Count > cur.Posted || cur.Posted > 0 {
			msgs = append(msgs, fmt.Sprintf("Raid sounds stopped%s: %d explosions over %s.", r.where(cur, false, size), cur.Count, fmtDur(cur.LastBlast.Sub(cur.Start))))
		}
		r.cur = nil
		return
	}
	if len(recent) == 0 {
		return
	}
	newest := recent[len(recent)-1]
	if cur == nil || newest.At.After(cur.LastBlast) {
		if cur == nil {
			cur = &raidState{Start: newest.At}
		}
		cur.LastBlast = newest.At
		cur.Count = 0
		for _, b := range r.blasts {
			if !b.At.Before(cur.Start) {
				cur.Count++
			}
		}
		if len(located) >= 2 {
			x, y, sup, ax, ay, alt := locate(located)
			if sup >= 2 {
				cur.Located, cur.X, cur.Y = true, x, y
				cur.Ambiguous = alt >= sup*8/10 && alt >= 2
				cur.AltX, cur.AltY = ax, ay
			}
		}
		r.cur = cur
	}
	// post: the first time 3+ explosions agree within a minute, then every 90 s while it goes on
	// first post: 4+ explosions in a minute that agree on a place (or 5+ when RustWatch can't
	// see where you are), then every 90 s while it goes on
	agree, need := 0, 5
	var lastMinLocated []raidBlast
	for _, b := range located {
		if now.Sub(b.At) <= time.Minute {
			lastMinLocated = append(lastMinLocated, b)
		}
	}
	if len(lastMinLocated) >= 4 {
		_, _, agree, _, _, _ = locate(lastMinLocated)
		need = 4
	} else if len(located) == 0 {
		agree = lastMin
	}
	switch {
	case cur.Posted == 0 && lastMin >= need && agree >= need:
	case cur.Posted > 0 && cur.Count >= cur.Posted+2 && now.Sub(cur.LastPost) >= 90*time.Second:
	default:
		return
	}
	cur.Posted, cur.LastPost = cur.Count, now
	return append(msgs, r.summary(recent, cur, size))
}

func (r *RaidWatch) summary(recent []raidBlast, cur *raidState, size uint32) string {
	counts := map[string]int{}
	var first time.Time
	for _, b := range recent {
		counts[b.Kind]++
		if first.IsZero() || b.At.Before(first) {
			first = b.At
		}
	}
	type kv struct {
		k string
		n int
	}
	var kinds []kv
	for k, n := range counts {
		kinds = append(kinds, kv{k, n})
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i].n > kinds[j].n })
	if len(kinds) > 2 { // the classification of individual blasts is rough; name the main ones
		kinds = kinds[:2]
	}
	var parts []string
	for _, x := range kinds {
		nm := raidKindNames[x.k]
		if nm[0] == "" {
			nm = [2]string{"explosion", "explosions"}
		}
		if x.n == 1 {
			parts = append(parts, "1 "+nm[0])
		} else {
			parts = append(parts, fmt.Sprintf("%d %s", x.n, nm[1]))
		}
	}
	span := fmtDur(recent[len(recent)-1].At.Sub(first))
	return fmt.Sprintf("Raid heard: %d explosions (mostly %s) in %s%s", len(recent), strings.Join(parts, ", "), span, r.where(cur, true, size))
}

// where describes the estimated spot relative to the player and as a grid.
func (r *RaidWatch) where(cur *raidState, detail bool, size uint32) string {
	var px, py float64
	have := false
	for i := len(r.blasts) - 1; i >= 0; i-- {
		if r.blasts[i].HavePose {
			px, py, have = r.blasts[i].PX, r.blasts[i].PY, true
			break
		}
	}
	if !cur.Located || !have {
		if detail {
			return ". Direction unknown: RustWatch needs Rust+ connected to see where you are and which way you face."
		}
		return ""
	}
	spot := func(x, y float64) (string, string) {
		d := math.Hypot(x-px, y-py)
		dir := compass(math.Atan2(x-px, y-py) * 180 / math.Pi)
		g := "?"
		if size > 0 {
			g = Grid(float32(x), float32(y), size)
		}
		return fmt.Sprintf("~%dm %s of you", int(math.Round(d/50))*50, dir), g
	}
	rel, g := spot(cur.X, cur.Y)
	if !detail {
		return " near " + g
	}
	out := ", " + rel + ", near " + g
	if cur.Ambiguous {
		rel2, g2 := spot(cur.AltX, cur.AltY)
		out += " (or " + rel2 + " near " + g2 + ": turn around between blasts to narrow it down)"
	}
	return out + "."
}

func compass(deg float64) string {
	deg = math.Mod(deg+360, 360)
	return []string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}[int((deg+22.5)/45)%8]
}

func (r *RaidWatch) post(text string) {
	log.Println("raid:", text)
	r.a.mu.Lock()
	r.a.alertLocked("raid", "Raid heard", text)
	r.a.mu.Unlock()
}

// RaidView is what the UI shows: recent explosions and the current estimate.
type RaidView struct {
	Templates int              `json:"templates"`
	Off       bool             `json:"off"`
	Blasts    []map[string]any `json:"blasts"`
	Active    *raidState       `json:"active"`
}

func (r *RaidWatch) view(off bool) RaidView {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := RaidView{Templates: r.ntpl, Off: off}
	for i := len(r.blasts) - 1; i >= 0 && len(v.Blasts) < 30; i-- {
		b := r.blasts[i]
		nm := raidKindNames[b.Kind][0]
		if nm == "" {
			nm = b.Kind
		}
		e := map[string]any{"time": b.At.Unix(), "kind": nm, "dist": b.Dist, "match": math.Round(b.Score*100) / 100,
			"side": fmt.Sprintf("%.0f° %s", math.Abs(b.Rel), map[bool]string{true: "right", false: "left"}[b.Rel >= 0])}
		if b.HavePose {
			e["bearings"] = []int{int(math.Mod(b.Yaw+b.Rel+360, 360)), int(math.Mod(b.Yaw+180-b.Rel+360, 360))}
		}
		v.Blasts = append(v.Blasts, e)
	}
	if r.cur != nil && time.Since(r.cur.LastBlast) < 10*time.Minute {
		c := *r.cur
		v.Active = &c
	}
	return v
}

// chatStatus answers !raid. Called with a.mu held, so it must not take a.mu itself.
func (r *RaidWatch) chatStatus() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil || time.Since(r.cur.LastBlast) > 10*time.Minute {
		if r.ntpl == 0 {
			return "Raid detection has no explosion sounds installed yet."
		}
		return "No raid heard in the last 10 minutes."
	}
	var size uint32
	if r.a.info != nil {
		size = r.a.info.MapSize
	}
	return fmt.Sprintf("Last raid heard %s ago: %d explosions%s", fmtDur(time.Since(r.cur.LastBlast)), r.cur.Count, r.where(r.cur, true, size))
}
