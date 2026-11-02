package app

import "time"

// funcLocal is a localSteamReader backed by a function (demo mode and tests).
type funcLocal func(hint uint64) localSnap

func (f funcLocal) Snapshot(hint uint64) localSnap { return f(hint) }
func (funcLocal) Close()                           {}

// DemoLocalSteam simulates what Steam gives a real PC: no full server list, but a
// "recently played with" list with real names. list returns (steamId, name) pairs for the
// server at addr; every third name is left for Steam to fill in.
func (a *App) DemoLocalSteam(list func() [][2]string, addr string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tracker.local = funcLocal(func(uint64) localSnap {
		snap := localSnap{State: "not-in-server", Note: "Steam has no full player list for this server", ServerAddr: addr}
		now := time.Now().Unix()
		for i, p := range list() {
			n := p[1]
			if i%3 == 2 {
				n = ""
			}
			snap.Recent = append(snap.Recent, localPlayer{ID: p[0], Name: n, At: now - int64(i*95)})
		}
		return snap
	})
}

// demoFullList makes the tracker read a full in-game Steam server list (tests).
func (a *App) demoFullList(list func() [][2]string, addr string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tracker.local = funcLocal(func(uint64) localSnap {
		snap := localSnap{State: "ok", ServerAddr: addr}
		for i, p := range list() {
			n := p[1]
			if i%3 == 2 {
				n = ""
			}
			snap.Players = append(snap.Players, localPlayer{ID: p[0], Name: n})
		}
		return snap
	})
}
