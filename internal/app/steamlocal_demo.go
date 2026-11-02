package app

// funcLocal is a localSteamReader backed by a function (demo mode and tests).
type funcLocal func(hint uint64) localSnap

func (f funcLocal) Snapshot(hint uint64) localSnap { return f(hint) }
func (funcLocal) Close()                           {}

// DemoLocalSteam makes the tracker read a simulated in-game Steam list: list returns
// (steamId, name) pairs for the server at addr. Every third name is left for Steam to fill in.
func (a *App) DemoLocalSteam(list func() [][2]string, addr string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tracker.local = funcLocal(func(uint64) localSnap {
		snap := localSnap{State: "ok", ServerAddr: addr, ServerName: "[EU] RustWatch Demo Server | Monthly"}
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
