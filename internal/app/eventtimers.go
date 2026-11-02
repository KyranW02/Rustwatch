package app

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Spawn forecasts for scheduled world events.
//
// Vanilla Rust runs patrol heli, cargo ship and the chinook crate drop on an EventSchedule that
// picks a random delay in a fixed window (2–4 h by default) and counts from server start. It does
// not depend on population (population only scales loot / ore / animal respawns). Because the
// delay is random, the honest prediction is a window: earliest .. latest possible spawn.
//
// RustWatch starts the first window from the wipe or a detected server restart, restarts it every
// time it sees the event, and replaces the defaults with the server's real intervals once it has
// seen a couple of spawns — which also catches modded servers and population-scaling plugins.

type Stamp struct {
	T      int64 `json:"t"`
	Approx bool  `json:"approx,omitempty"` // already out when first seen
}

var forecastKinds = []struct{ Key, Label string }{
	{"heli", "Patrol heli"}, {"cargo", "Cargo ship"}, {"chinook", "Chinook crate drop"}, {"vendor", "Travelling vendor"},
}

func defaultWindows() map[string][2]float64 {
	return map[string][2]float64{"heli": {2, 4}, "cargo": {2, 4}, "chinook": {2, 4}, "vendor": {2, 4}}
}

type Forecast struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	State    string `json:"state"` // active, upcoming, window, overdue, unknown
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	Text     string `json:"text"`
	Short    string `json:"short"`
	Source   string `json:"source"`
	Learned  bool   `json:"learned"`
	LastSeen int64  `json:"lastSeen"`
}

// recordSpawnLocked stores a sighting. Caller holds a.mu.
func (a *App) recordSpawnLocked(kind string, at time.Time, approx bool) {
	if a.srv == nil {
		return
	}
	if a.srv.EventLog == nil {
		a.srv.EventLog = map[string][]Stamp{}
	}
	log := a.srv.EventLog[kind]
	// ignore duplicate reports (e.g. reconnect while the same event is still out)
	if n := len(log); n > 0 && at.Unix()-log[n-1].T < 20*60 {
		return
	}
	log = append(log, Stamp{T: at.Unix(), Approx: approx})
	if len(log) > 40 {
		log = log[len(log)-40:]
	}
	a.srv.EventLog[kind] = log
	a.saveLocked()
}

// recordRestartLocked notes that the server (and so its event schedule) restarted.
func (a *App) recordRestartLocked(at time.Time) {
	if a.srv == nil {
		return
	}
	for _, r := range a.srv.Restarts {
		if math.Abs(float64(r-at.Unix())) < 10*60 {
			return
		}
	}
	a.srv.Restarts = append(a.srv.Restarts, at.Unix())
	if len(a.srv.Restarts) > 20 {
		a.srv.Restarts = a.srv.Restarts[len(a.srv.Restarts)-20:]
	}
	a.saveLocked()
	a.alertLog.Printf("[restart] server restart detected at %s — event timers restarted", at.Format("15:04"))
}

// learnedWindow derives the server's real spawn interval from consecutive exact sightings.
func learnedWindow(log []Stamp, restarts []int64, def [2]float64) (lo, hi time.Duration, n int) {
	maxGap := time.Duration(def[1]*3) * time.Hour
	var iv []time.Duration
	for i := 1; i < len(log); i++ {
		a, b := log[i-1], log[i]
		if a.Approx || b.Approx {
			continue
		}
		restarted := false
		for _, r := range restarts {
			if r > a.T && r < b.T {
				restarted = true
			}
		}
		d := time.Duration(b.T-a.T) * time.Second
		if restarted || d < 10*time.Minute || d > maxGap {
			continue
		}
		iv = append(iv, d)
	}
	if len(iv) < 2 {
		return 0, 0, len(iv)
	}
	sort.Slice(iv, func(i, j int) bool { return iv[i] < iv[j] })
	med := iv[len(iv)/2]
	lo, hi = time.Duration(float64(iv[0])*0.95), time.Duration(float64(iv[len(iv)-1])*1.05)
	if hi-lo < med/5 { // very regular server: keep at least a ±10% window
		lo, hi = med-med/10, med+med/10
	}
	return lo, hi, len(iv)
}

func clock(t time.Time) string { return t.Format("15:04") }

// forecastLocked predicts the next spawn of kind. Caller holds a.mu.
func (a *App) forecastLocked(kind, label string, now time.Time) Forecast {
	f := Forecast{Kind: kind, Label: label, State: "unknown"}
	if a.srv == nil {
		return f
	}
	def, ok := a.cfg.Settings.EventWindows[kind]
	if !ok || def[1] <= 0 {
		def = defaultWindows()[kind]
	}
	lo, hi := time.Duration(def[0]*float64(time.Hour)), time.Duration(def[1]*float64(time.Hour))
	f.Source = fmt.Sprintf("Rust default: every %g–%g h", def[0], def[1])
	log := a.srv.EventLog[kind]
	if l, h, n := learnedWindow(log, a.srv.Restarts, def); n >= 2 {
		lo, hi, f.Learned = l, h, true
		f.Source = fmt.Sprintf("Learned from %d spawns on this server: every %s–%s", n+1, fmtDurShort(l), fmtDurShort(h))
	}
	var base time.Time
	baseWhat := ""
	if n := len(log); n > 0 {
		base, baseWhat = time.Unix(log[n-1].T, 0), "last seen"
		f.LastSeen = log[n-1].T
	}
	for _, r := range a.srv.Restarts {
		if t := time.Unix(r, 0); t.After(base) {
			base, baseWhat = t, "server restart"
		}
	}
	if a.info != nil && a.info.WipeTime > 0 {
		if t := time.Unix(int64(a.info.WipeTime), 0); t.After(base) {
			base, baseWhat = t, "wipe"
		}
	}
	active := a.events != nil && a.events.isActive(kind)
	if base.IsZero() {
		if active {
			f.State, f.Text, f.Short = "active", label+" is on the map now.", "on the map now"
		} else {
			f.Text, f.Short = "No timing yet — RustWatch will time it from the first sighting.", "waiting for first sighting"
		}
		return f
	}
	from, to := base.Add(lo), base.Add(hi)
	f.From, f.To = from.Unix(), to.Unix()
	after := " (timed from the " + baseWhat + " at " + clock(base) + ")"
	switch {
	case active:
		f.State = "active"
		f.Short = "on the map now · next " + clock(from) + "–" + clock(to)
		f.Text = fmt.Sprintf("%s is on the map now. Next one expected between %s and %s.", label, clock(from), clock(to))
	case now.Before(from):
		f.State = "upcoming"
		f.Short = fmt.Sprintf("in %s – %s", fmtDurShort(from.Sub(now)), fmtDurShort(to.Sub(now)))
		f.Text = fmt.Sprintf("Next %s expected in %s to %s (%s–%s)%s.", strings.ToLower(label), fmtDurShort(from.Sub(now)), fmtDurShort(to.Sub(now)), clock(from), clock(to), after)
	case now.Before(to):
		f.State = "window"
		f.Short = "could spawn any time · latest ~" + clock(to)
		f.Text = fmt.Sprintf("%s could spawn any time now — latest around %s (%s)%s.", label, clock(to), fmtDurShort(to.Sub(now)), after)
	case a.connectedAt.IsZero() || a.connectedAt.After(from):
		// RustWatch wasn't watching when it was due, so one most likely came and went unseen
		f.State = "unknown"
		f.Short = "missed while RustWatch was closed · times from next sighting"
		f.Text = fmt.Sprintf("%s was due %s–%s while RustWatch wasn't watching, so one probably came and went. Timing restarts from the next sighting.", label, clock(from), clock(to))
	default:
		f.State = "overdue"
		f.Short = "overdue by " + fmtDurShort(now.Sub(to))
		f.Text = fmt.Sprintf("%s is overdue by %s — it may have spawned while RustWatch was closed, or this server runs it less often.", label, fmtDurShort(now.Sub(to)))
	}
	return f
}

func (a *App) forecastsLocked() []Forecast {
	now := time.Now()
	var out []Forecast
	for _, k := range forecastKinds {
		out = append(out, a.forecastLocked(k.Key, k.Label, now))
	}
	return out
}

// checkWindowsLocked alerts once when an event's spawn window opens. Caller holds a.mu.
func (a *App) checkWindowsLocked() {
	if a.srv == nil || a.status != "connected" {
		return
	}
	if a.windowAlerted == nil {
		a.windowAlerted = map[string]int64{}
	}
	for _, f := range a.forecastsLocked() {
		if f.State != "window" || a.windowAlerted[f.Kind] == f.From {
			continue
		}
		a.windowAlerted[f.Kind] = f.From
		// don't announce windows that opened long ago (e.g. right after starting RustWatch)
		if time.Since(time.Unix(f.From, 0)) > 10*time.Minute {
			continue
		}
		a.alertLocked("event_window", f.Label+" due", fmt.Sprintf("%s could spawn any time now (latest ~%s).", f.Label, clock(time.Unix(f.To, 0))))
	}
}

func fmtDurShort(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	d = d.Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

func (e *EventEngine) isActive(kind string) bool {
	switch kind {
	case "heli":
		return len(e.heli) > 0
	case "cargo":
		return len(e.cargo) > 0
	case "vendor":
		return len(e.vendors) > 0
	case "chinook":
		for _, c := range e.ch47 {
			if c.kind == "crate" {
				return true
			}
		}
	}
	return false
}
