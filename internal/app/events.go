package app

import (
	"fmt"
	"strings"
	"time"

	"rustwatch/internal/rustplus"
)

// EventEngine diffs successive map-marker snapshots and detects world events
// (cargo, patrol heli, chinook / locked crates, oil rigs, bradley, travelling vendor).
type EventEngine struct {
	MapSize   uint32
	Monuments []rustplus.Monument
	Now       func() time.Time
	Emit      func(kind, title, text string)
	// OnSpawn reports an event appearing: approx is true when it was already out when first seen.
	OnSpawn func(kind string, at time.Time, approx bool)

	LockedCrate    time.Duration
	CargoEgress    time.Duration
	BradleyRespawn time.Duration

	cargo   map[uint32]*cargoState
	heli    map[uint32]*tracked
	ch47    map[uint32]*tracked
	crates  map[uint32]*tracked
	vendors map[uint32]*tracked
	explos  map[uint32]bool
	first   bool

	CargoLeft     time.Time
	HeliLeft      time.Time
	HeliDown      time.Time
	HeliDownWhere string
	CrateDrop     time.Time
	CrateWhere    string
	OilSmall      time.Time
	OilLarge      time.Time
	oilSmallDone  bool
	oilLargeDone  bool
	BradleyDown   time.Time
	bradleyNoted  bool
	VendorLeft    time.Time

	// heard in game (sound detection), since Rust+ no longer sends these events
	CargoHeard     time.Time
	VendorHeard    time.Time
	ExcavatorHeard time.Time
	DeepSeaHeard   time.Time
	DeepSeaClosing time.Time
	OilReset       time.Time
	OilCalled      time.Time // an oil rig alarm heard nearby (which rig isn't known)
	oilCalledDone  bool
}

type tracked struct {
	first time.Time
	x, y  float32
	kind  string
}

type cargoState struct {
	tracked
	atEdge       bool
	docked       bool
	dockedAt     time.Time
	harbor       string
	egressNotice bool
}

func NewEventEngine() *EventEngine {
	e := &EventEngine{Now: time.Now, LockedCrate: 15 * time.Minute, CargoEgress: 50 * time.Minute, BradleyRespawn: 60 * time.Minute}
	e.Reset()
	return e
}

func (e *EventEngine) Reset() {
	e.cargo = map[uint32]*cargoState{}
	e.heli = map[uint32]*tracked{}
	e.ch47 = map[uint32]*tracked{}
	e.crates = map[uint32]*tracked{}
	e.vendors = map[uint32]*tracked{}
	e.explos = map[uint32]bool{}
	e.first = true
}

func (e *EventEngine) loc(x, y float32) string { return Location(x, y, e.MapSize, e.Monuments) }

func (e *EventEngine) monuments(substr ...string) []rustplus.Monument {
	var out []rustplus.Monument
	for _, m := range e.Monuments {
		for _, s := range substr {
			if strings.Contains(m.Token, s) {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

func (e *EventEngine) nearMonument(x, y float32, maxDist float64, substr ...string) (rustplus.Monument, bool) {
	for _, m := range e.monuments(substr...) {
		if dist(x, y, m.X, m.Y) <= maxDist {
			return m, true
		}
	}
	return rustplus.Monument{}, false
}

func (e *EventEngine) spawned(kind string, now time.Time, exact bool) {
	if e.OnSpawn != nil {
		e.OnSpawn(kind, now, !exact || e.first)
	}
}

func (e *EventEngine) emit(kind, title, text string) {
	if e.Emit != nil && !e.first {
		e.Emit(kind, title, text)
	}
}

// Update processes a new marker snapshot.
func (e *EventEngine) Update(markers []rustplus.Marker) {
	now := e.Now()
	seen := map[uint32]bool{}
	edge := 4 * gridSize

	for _, m := range markers {
		seen[m.ID] = true
		switch m.Type {
		case rustplus.MarkerCargoShip:
			c, ok := e.cargo[m.ID]
			if !ok {
				c = &cargoState{tracked: tracked{first: now}}
				c.atEdge = outsideGrid(m.X, m.Y, e.MapSize, -gridSize) || outsideGrid(m.X, m.Y, e.MapSize, 0)
				e.cargo[m.ID] = c
				e.spawned("cargo", now, c.atEdge)
				if c.atEdge {
					e.emit("cargo", "Cargo ship spawned", "Cargo Ship entered the map at "+e.loc(m.X, m.Y))
				} else {
					e.emit("cargo", "Cargo ship located", "Cargo Ship is at "+e.loc(m.X, m.Y))
				}
			}
			c.x, c.y = m.X, m.Y
			if h, ok := e.nearMonument(m.X, m.Y, 100, "harbor"); ok && !c.docked {
				c.docked, c.dockedAt, c.harbor = true, now, monumentName(h.Token)
				e.emit("cargo", "Cargo ship docked", fmt.Sprintf("Cargo Ship docked at %s (%s)", c.harbor, Grid(h.X, h.Y, e.MapSize)))
			} else if !ok && c.docked && now.Sub(c.dockedAt) > 30*time.Second {
				c.docked = false
				e.emit("cargo", "Cargo ship left harbor", "Cargo Ship left "+c.harbor)
			}
			if c.atEdge && !c.egressNotice && now.Sub(c.first) >= e.CargoEgress {
				c.egressNotice = true
				e.emit("cargo", "Cargo ship leaving", "Cargo Ship is egressing — crates stop spawning soon. At "+e.loc(m.X, m.Y))
			}
		case rustplus.MarkerPatrolHelicopter:
			h, ok := e.heli[m.ID]
			if !ok {
				h = &tracked{first: now}
				e.heli[m.ID] = h
				e.spawned("heli", now, true)
				e.emit("heli", "Patrol helicopter spawned", "Patrol Heli is in at "+e.loc(m.X, m.Y))
			}
			h.x, h.y = m.X, m.Y
		case rustplus.MarkerCH47:
			c, ok := e.ch47[m.ID]
			if !ok {
				c = &tracked{first: now, kind: "crate"}
				e.ch47[m.ID] = c
				if rig, ok := e.nearMonument(m.X, m.Y, 550, "oil_rig_small"); ok {
					c.kind = "small"
					e.OilSmall, e.oilSmallDone = now, false
					e.emit("oilrig", "Small Oil Rig triggered", fmt.Sprintf("Heavy scientists called to Small Oil Rig (%s). Crate unlocks in %d min.", Grid(rig.X, rig.Y, e.MapSize), int(e.LockedCrate.Minutes())))
				} else if rig, ok := e.nearMonument(m.X, m.Y, 550, "large_oil_rig"); ok {
					c.kind = "large"
					e.OilLarge, e.oilLargeDone = now, false
					e.emit("oilrig", "Large Oil Rig triggered", fmt.Sprintf("Heavy scientists called to Large Oil Rig (%s). Crate unlocks in %d min.", Grid(rig.X, rig.Y, e.MapSize), int(e.LockedCrate.Minutes())))
				} else if outsideGrid(m.X, m.Y, e.MapSize, -gridSize) || outsideGrid(m.X, m.Y, e.MapSize, 0) {
					e.spawned("chinook", now, true)
					e.emit("chinook", "Chinook entered the map", "Chinook (CH47) entered at "+e.loc(m.X, m.Y))
				} else {
					e.spawned("chinook", now, false)
					e.emit("chinook", "Chinook located", "Chinook (CH47) is at "+e.loc(m.X, m.Y))
				}
			}
			c.x, c.y = m.X, m.Y
		case rustplus.MarkerCrate:
			if _, ok := e.crates[m.ID]; !ok {
				e.crates[m.ID] = &tracked{first: now, x: m.X, y: m.Y}
				if _, onRig := e.nearMonument(m.X, m.Y, 150, "oil_rig"); !onRig {
					e.CrateDrop, e.CrateWhere = now, e.loc(m.X, m.Y)
					e.emit("chinook", "Locked crate dropped", "Chinook dropped a locked crate at "+e.CrateWhere)
				}
			}
		case rustplus.MarkerTravelingVendor:
			v, ok := e.vendors[m.ID]
			if !ok {
				v = &tracked{first: now}
				e.vendors[m.ID] = v
				e.spawned("vendor", now, true)
				e.emit("vendor", "Travelling vendor spawned", "Travelling Vendor is at "+e.loc(m.X, m.Y))
			}
			v.x, v.y = m.X, m.Y
		case rustplus.MarkerExplosion:
			if !e.explos[m.ID] {
				e.explos[m.ID] = true
				if _, ok := e.nearMonument(m.X, m.Y, 350, "launchsite", "launch_site"); ok {
					e.BradleyDown, e.bradleyNoted = now, false
					e.emit("bradley", "Bradley destroyed", fmt.Sprintf("Bradley APC was destroyed at Launch Site. Respawn in ~%d min.", int(e.BradleyRespawn.Minutes())))
				} else if len(e.heli) > 0 || now.Sub(e.HeliDown) < 2*time.Minute {
					if now.Sub(e.HeliDown) > 2*time.Minute {
						e.HeliDown, e.HeliDownWhere = now, e.loc(m.X, m.Y)
						e.emit("heli", "Patrol heli taken down", "Patrol Heli went down at "+e.HeliDownWhere)
					}
				}
			}
		}
	}

	// departures
	for id, c := range e.cargo {
		if !seen[id] {
			delete(e.cargo, id)
			e.CargoLeft = now
			e.emit("cargo", "Cargo ship left", "Cargo Ship left the map")
			_ = c
		}
	}
	for id, h := range e.heli {
		if !seen[id] {
			delete(e.heli, id)
			if outsideGrid(h.x, h.y, e.MapSize, -edge/4) || outsideGrid(h.x, h.y, e.MapSize, 0) {
				e.HeliLeft = now
				e.emit("heli", "Patrol heli left", "Patrol Heli left the map")
			} else if now.Sub(e.HeliDown) > 2*time.Minute {
				e.HeliDown, e.HeliDownWhere = now, e.loc(h.x, h.y)
				e.emit("heli", "Patrol heli taken down", "Patrol Heli went down at "+e.HeliDownWhere)
			}
		}
	}
	for id := range e.ch47 {
		if !seen[id] {
			delete(e.ch47, id)
		}
	}
	for id := range e.crates {
		if !seen[id] {
			delete(e.crates, id)
		}
	}
	for id := range e.explos {
		if !seen[id] {
			delete(e.explos, id)
		}
	}
	for id := range e.vendors {
		if !seen[id] {
			delete(e.vendors, id)
			e.VendorLeft = now
			e.emit("vendor", "Travelling vendor left", "Travelling Vendor left the map")
		}
	}
	e.Tick()
	e.first = false
}

// Tick fires time-based alerts (oil rig crate unlocks, bradley respawn).
func (e *EventEngine) Tick() {
	now := e.Now()
	if !e.OilSmall.IsZero() && !e.oilSmallDone && now.Sub(e.OilSmall) >= e.LockedCrate {
		e.oilSmallDone = true
		e.emit("oilrig", "Small Oil Rig crate unlocked", "Small Oil Rig locked crate is now unlocked")
	}
	if !e.OilLarge.IsZero() && !e.oilLargeDone && now.Sub(e.OilLarge) >= e.LockedCrate {
		e.oilLargeDone = true
		e.emit("oilrig", "Large Oil Rig crate unlocked", "Large Oil Rig locked crate is now unlocked")
	}
	if !e.OilCalled.IsZero() && !e.oilCalledDone && now.Sub(e.OilCalled) >= e.LockedCrate {
		e.oilCalledDone = true
		e.emit("oilrig", "Oil rig crate unlocked", "The oil rig crate you heard being triggered should now be unlocked")
	}
	if !e.BradleyDown.IsZero() && !e.bradleyNoted && now.Sub(e.BradleyDown) >= e.BradleyRespawn {
		e.bradleyNoted = true
		e.emit("bradley", "Bradley should be back", "Bradley APC should have respawned at Launch Site")
	}
}

func ago(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "never seen"
	}
	return fmtDur(now.Sub(t)) + " ago"
}

func fmtDur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h >= 24:
		return fmt.Sprintf("%dd %dh", h/24, h%24)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// Heard records an event recognised by its in-game sound.
func (e *EventEngine) Heard(cue string) {
	now := e.Now()
	first := e.first
	e.first = false // a sound is live news, never "already out at startup"
	defer func() { e.first = first }()
	switch cue {
	case "cargo":
		e.CargoHeard = now
		e.spawned("cargo", now, false) // a sighting, but not necessarily the moment it spawned
		e.emit("cargo", "Cargo ship heard", "Cargo Ship horn heard in game, cargo is out")
	case "oil_small":
		e.OilSmall, e.oilSmallDone = now, false
		e.emit("oilrig", "Small Oil Rig triggered", fmt.Sprintf("Heard Small Oil Rig being called in. Crate unlocks in %d min.", int(e.LockedCrate.Minutes())))
	case "oil_large":
		e.OilLarge, e.oilLargeDone = now, false
		e.emit("oilrig", "Large Oil Rig triggered", fmt.Sprintf("Heard Large Oil Rig being called in. Crate unlocks in %d min.", int(e.LockedCrate.Minutes())))
	case "bradley":
		e.BradleyDown, e.bradleyNoted = now, false
		e.emit("bradley", "Bradley destroyed", fmt.Sprintf("Heard Bradley being destroyed. Respawns in ~%d min.", int(e.BradleyRespawn.Minutes())))
	case "heli_down":
		e.HeliDown, e.HeliDownWhere = now, "near you"
		e.emit("heli", "Patrol heli shot down", "Heard the patrol heli go down nearby")
	case "vendor":
		if e.VendorHeard.IsZero() || now.Sub(e.VendorHeard) > 40*time.Minute {
			e.spawned("vendor", now, false)
		}
		e.VendorHeard = now
		e.emit("vendor", "Travelling vendor heard", "Heard the Travelling Vendor nearby")
	case "oil_reset":
		e.OilReset = now
		e.emit("oilrig", "Oil rig reset", "Heard an oil rig reset: fresh crate and scientists are back.")
	case "oil_called":
		e.OilCalled, e.oilCalledDone = now, false
		e.emit("oilrig", "Oil rig alarm", fmt.Sprintf("Heard an oil rig alarm nearby. Crate unlocks in about %d min.", int(e.LockedCrate.Minutes())))
	case "deepsea_close":
		e.DeepSeaClosing = now
		e.emit("deepsea", "Deep Sea closing", "Heard the Deep Sea closing alarm.")
	case "excavator":
		e.ExcavatorHeard = now
		e.emit("excavator", "Giant excavator started", "Heard the giant excavator start up")
	case "deepsea":
		e.DeepSeaHeard = now
		e.emit("deepsea", "Deep Sea alarm", "Heard the Deep Sea alarm: it's opening or about to close")
	}
}

// heardLine describes an event last known from its sound.
func heardLine(name string, t, now time.Time, lasts time.Duration) string {
	if t.IsZero() {
		return ""
	}
	if now.Sub(t) < lasts {
		return fmt.Sprintf(" Heard %s, probably still out (about %s left).", ago(t, now), fmtDur(lasts-now.Sub(t)))
	}
	return fmt.Sprintf(" Last heard %s.", ago(t, now))
}

func (e *EventEngine) ExcavatorStatus() string {
	if e.ExcavatorHeard.IsZero() {
		return "Giant excavator: not heard since RustWatch started."
	}
	return "Giant excavator: started " + ago(e.ExcavatorHeard, e.Now()) + "."
}

func (e *EventEngine) DeepSeaStatus() string {
	now := e.Now()
	switch {
	case !e.DeepSeaClosing.IsZero() && e.DeepSeaClosing.After(e.DeepSeaHeard):
		return "Deep Sea: closing alarm heard " + ago(e.DeepSeaClosing, now) + "."
	case !e.DeepSeaHeard.IsZero():
		return "Deep Sea: alarm heard " + ago(e.DeepSeaHeard, now) + " (opening or closing)."
	}
	return "Deep Sea: not heard since RustWatch started."
}

// OilHeardStatus describes oil rig sounds heard (reset / alarm), "" if none.
func (e *EventEngine) OilHeardStatus() string {
	now := e.Now()
	var parts []string
	if !e.OilCalled.IsZero() {
		s := "Oil rig alarm heard " + ago(e.OilCalled, now)
		if left := e.LockedCrate - now.Sub(e.OilCalled); left > 0 {
			s += fmt.Sprintf(", crate unlocks in %s", fmtDur(left))
		}
		parts = append(parts, s+".")
	}
	if !e.OilReset.IsZero() {
		parts = append(parts, "An oil rig reset "+ago(e.OilReset, now)+".")
	}
	return strings.Join(parts, " ")
}

// Status lines used by chat commands and the dashboard.

func (e *EventEngine) CargoStatus() string {
	now := e.Now()
	if len(e.cargo) == 0 {
		if !e.CargoHeard.IsZero() && (e.CargoLeft.IsZero() || e.CargoHeard.After(e.CargoLeft)) {
			return "Cargo Ship:" + heardLine("cargo", e.CargoHeard, now, e.CargoEgress+10*time.Minute)
		}
		return "Cargo Ship is not out. Last seen " + ago(e.CargoLeft, now) + "."
	}
	var parts []string
	for _, c := range e.cargo {
		s := fmt.Sprintf("Cargo Ship at %s, out for %s", e.loc(c.x, c.y), fmtDur(now.Sub(c.first)))
		if c.docked {
			s += ", docked at " + c.harbor
		}
		if c.atEdge {
			left := e.CargoEgress - now.Sub(c.first)
			if left > 0 {
				s += fmt.Sprintf(", egress in ~%s", fmtDur(left))
			} else {
				s += ", egressing"
			}
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " | ")
}

func (e *EventEngine) HeliStatus() string {
	now := e.Now()
	if len(e.heli) > 0 {
		var parts []string
		for _, h := range e.heli {
			parts = append(parts, fmt.Sprintf("Patrol Heli at %s, out for %s", e.loc(h.x, h.y), fmtDur(now.Sub(h.first))))
		}
		return strings.Join(parts, " | ")
	}
	s := "Patrol Heli is not out."
	if !e.HeliDown.IsZero() && (e.HeliLeft.IsZero() || e.HeliDown.After(e.HeliLeft)) {
		s += fmt.Sprintf(" Last taken down %s at %s.", ago(e.HeliDown, now), e.HeliDownWhere)
	} else if !e.HeliLeft.IsZero() {
		s += " Last left " + ago(e.HeliLeft, now) + "."
	}
	return s
}

func (e *EventEngine) ChinookStatus() string {
	now := e.Now()
	s := ""
	for _, c := range e.ch47 {
		if c.kind == "crate" {
			s = fmt.Sprintf("Chinook is out at %s. ", e.loc(c.x, c.y))
		}
	}
	if e.CrateDrop.IsZero() {
		return s + "No locked crate drop seen yet."
	}
	return s + fmt.Sprintf("Last locked crate dropped %s at %s.", ago(e.CrateDrop, now), e.CrateWhere)
}

func (e *EventEngine) oilStatus(name string, t time.Time) string {
	now := e.Now()
	if t.IsZero() {
		return name + ": not triggered since the bot started."
	}
	left := e.LockedCrate - now.Sub(t)
	if left > 0 {
		return fmt.Sprintf("%s: triggered %s, crate unlocks in %s.", name, ago(t, now), fmtDur(left))
	}
	return fmt.Sprintf("%s: triggered %s, crate is unlocked.", name, ago(t, now))
}

func (e *EventEngine) SmallOilStatus() string { return e.oilStatus("Small Oil Rig", e.OilSmall) }
func (e *EventEngine) LargeOilStatus() string { return e.oilStatus("Large Oil Rig", e.OilLarge) }

func (e *EventEngine) BradleyStatus() string {
	now := e.Now()
	if e.BradleyDown.IsZero() {
		return "Bradley has not been destroyed since the bot started."
	}
	left := e.BradleyRespawn - now.Sub(e.BradleyDown)
	if left > 0 {
		return fmt.Sprintf("Bradley destroyed %s, respawns in ~%s.", ago(e.BradleyDown, now), fmtDur(left))
	}
	return fmt.Sprintf("Bradley destroyed %s, should be back now.", ago(e.BradleyDown, now))
}

func (e *EventEngine) VendorStatus() string {
	now := e.Now()
	if len(e.vendors) == 0 {
		if !e.VendorHeard.IsZero() && (e.VendorLeft.IsZero() || e.VendorHeard.After(e.VendorLeft)) {
			return "Travelling Vendor:" + heardLine("vendor", e.VendorHeard, now, 30*time.Minute)
		}
		return "Travelling Vendor is not out. Last left " + ago(e.VendorLeft, now) + "."
	}
	var parts []string
	for _, v := range e.vendors {
		parts = append(parts, fmt.Sprintf("Travelling Vendor at %s, out for %s", e.loc(v.x, v.y), fmtDur(now.Sub(v.first))))
	}
	return strings.Join(parts, " | ")
}

// Summary for the dashboard.
type EventSummary struct {
	Cargo     string   `json:"cargo"`
	Heli      string   `json:"heli"`
	Chinook   string   `json:"chinook"`
	Small     string   `json:"smallOil"`
	Large     string   `json:"largeOil"`
	Bradley   string   `json:"bradley"`
	Vendor    string   `json:"vendor"`
	Excavator string   `json:"excavator"`
	OilHeard  string   `json:"oilHeard"`
	DeepSea   string   `json:"deepsea"`
	Active    []string `json:"active"`
}

func (e *EventEngine) Summary() EventSummary {
	s := EventSummary{Cargo: e.CargoStatus(), Heli: e.HeliStatus(), Chinook: e.ChinookStatus(),
		Small: e.SmallOilStatus(), Large: e.LargeOilStatus(), Bradley: e.BradleyStatus(), Vendor: e.VendorStatus(),
		Excavator: e.ExcavatorStatus(), DeepSea: e.DeepSeaStatus(), OilHeard: e.OilHeardStatus()}
	if s.OilHeard == "" {
		s.OilHeard = "No oil rig sounds heard yet."
	}
	now := e.Now()
	if len(e.cargo) > 0 || (!e.CargoHeard.IsZero() && now.Sub(e.CargoHeard) < e.CargoEgress+10*time.Minute) {
		s.Active = append(s.Active, "cargo")
	}
	if len(e.heli) > 0 {
		s.Active = append(s.Active, "heli")
	}
	if len(e.vendors) > 0 || (!e.VendorHeard.IsZero() && now.Sub(e.VendorHeard) < 30*time.Minute) {
		s.Active = append(s.Active, "vendor")
	}
	if !e.OilSmall.IsZero() && now.Sub(e.OilSmall) < e.LockedCrate {
		s.Active = append(s.Active, "smallOil")
	}
	if !e.OilLarge.IsZero() && now.Sub(e.OilLarge) < e.LockedCrate {
		s.Active = append(s.Active, "largeOil")
	}
	return s
}
