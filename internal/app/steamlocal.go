package app

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

// The Steam client on your PC knows who is on the game server you're connected to — it's the
// list in the Steam overlay's Players tab, with real Steam names and IDs, even on servers that
// hide names from their public player list. While Rust is running, RustWatch reads that list
// through the Steamworks API (Windows only; see steamlocal_windows.go).

type localPlayer struct {
	ID   string // SteamID64
	Name string // "" until Steam has the name
	At   int64  // recently-played-with time (Unix), for Recent entries
}

type localSnap struct {
	State      string // "ok", "no-rust", "not-in-server", "unavailable"
	Note       string // human explanation for anything but "ok"
	ServerAddr string // ip:port of the server you're in, as Steam reports it
	ServerName string
	Players    []localPlayer // everyone on the server, when Steam has that list
	Recent     []localPlayer // Steam's "recently played with" for Rust (max 100), with times
}

// localSteamReader is implemented by the Windows Steam client reader (and fakes in tests).
type localSteamReader interface {
	// Snapshot returns who is on the server you're in. hint is the active server's Steam ID
	// (from its query reply), used when Steam doesn't say which server you're on.
	Snapshot(hint uint64) localSnap
	Close()
}

// bySteam reports whether tracked players with a Steam ID are keyed by it ("s:<id>").
func (t *Tracker) bySteam() bool { return t.steamOn || t.localMode }

// judgeLocal decides whether the Steam client's list is for the active server. Called without a.mu.
func (t *Tracker) judgeLocal(snap localSnap, sa serverAddrs, have bool) (bool, string) {
	if !have {
		return false, ""
	}
	switch snap.State {
	case "ok":
	case "no-rust":
		return false, "Rust isn't running, so the in-game Steam list isn't available"
	default:
		return false, snap.Note
	}
	if snap.ServerAddr != "" {
		sa.resolve(context.Background())
		if h, _, err := net.SplitHostPort(snap.ServerAddr); err == nil && !sa.ips[net.ParseIP(h).String()] {
			name := snap.ServerName
			if name == "" {
				name = snap.ServerAddr
			}
			return false, "you're on " + name + ", not the server RustWatch is showing"
		}
	}
	return true, fmt.Sprintf("%d players from your Steam client (the Steam overlay's player list)", len(snap.Players))
}

// fillNames looks up names Steam hadn't sent yet (Web API, if a key is set) into the cache.
func (t *Tracker) fillNames(ctx context.Context, key string, ps []localPlayer) {
	t.a.mu.Lock()
	var missing []string
	for _, p := range ps {
		if p.Name != "" {
			t.names[p.ID] = p.Name
		} else if t.names[p.ID] == "" && len(missing) < 100 {
			missing = append(missing, p.ID)
		}
	}
	t.a.mu.Unlock()
	if key == "" || len(missing) == 0 {
		return
	}
	sum, err := fetchSummaries(ctx, key, missing)
	if err != nil {
		return
	}
	t.a.mu.Lock()
	for id, s := range sum {
		if s.PersonaName != "" {
			t.names[id] = s.PersonaName
		}
	}
	t.a.mu.Unlock()
}

// localListLocked turns the Steam client's list into the online list, borrowing session
// lengths from the server's own list where names match, and links name-tracked players to
// their Steam ID when their name appears. Caller holds a.mu.
func (t *Tracker) localListLocked(sc *ServerConfig, ps []localPlayer, serverList map[string]seenPlayer) map[string]seenPlayer {
	cur := map[string]seenPlayer{}
	byName := map[string]string{}
	for _, p := range ps {
		name := cleanName(t.names[p.ID])
		if name == "" {
			for _, tp := range sc.Tracked {
				if tp.SteamID == p.ID {
					name = tp.Name
				}
			}
		}
		if name == "" {
			name = "Steam user " + p.ID[max(0, len(p.ID)-5):]
		}
		sp := seenPlayer{Name: name}
		if x, ok := serverList[directKey(name)]; ok {
			sp.Dur = x.Dur
		}
		cur["s:"+p.ID] = sp
		byName[strings.ToLower(name)] = p.ID
	}
	for i := range sc.Tracked {
		tp := &sc.Tracked[i]
		if tp.SteamID == "" && tp.Name != "" {
			if id, ok := byName[strings.ToLower(strings.TrimSpace(tp.Name))]; ok {
				tp.SteamID = id
			}
		}
	}
	return cur
}

// recentUsable: the recently-played-with list is worth showing when Rust is running and
// you're on the active server (or Steam doesn't say which server you're on).
func (t *Tracker) recentUsable(snap localSnap, sa serverAddrs) bool {
	if len(snap.Recent) == 0 || snap.State == "" || snap.State == "no-rust" || snap.State == "unavailable" {
		return false
	}
	if snap.ServerAddr != "" {
		sa.resolve(context.Background())
		if h, _, err := net.SplitHostPort(snap.ServerAddr); err == nil && !sa.ips[net.ParseIP(h).String()] {
			return false
		}
	}
	return true
}

// storeRecentLocked keeps the latest recently-played-with list with names filled in, and links
// players tracked by name to their Steam ID when the name matches. Caller holds a.mu.
func (t *Tracker) storeRecentLocked(sc *ServerConfig, rs []localPlayer) {
	t.recent, t.recentAt = nil, time.Now()
	byName := map[string]string{}
	for _, r := range rs {
		if r.Name == "" {
			r.Name = t.names[r.ID]
		}
		t.recent = append(t.recent, r)
		if r.Name != "" {
			byName[strings.ToLower(cleanName(r.Name))] = r.ID
		}
	}
	changed := false
	for i := range sc.Tracked {
		tp := &sc.Tracked[i]
		if tp.SteamID == "" && tp.Name != "" {
			if id, ok := byName[strings.ToLower(strings.TrimSpace(tp.Name))]; ok {
				tp.SteamID, changed = id, true
			}
		}
	}
	if changed {
		t.a.saveLocked()
	}
}

type RecentView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	At      int64  `json:"at"`
	Ago     string `json:"ago"`
	Tracked bool   `json:"tracked"`
	Tag     string `json:"tag"`
	Color   string `json:"color"`
}

func (t *Tracker) recentViewLocked(tmap map[string]*TrackedPlayer) []RecentView {
	now := time.Now()
	var out []RecentView
	for _, r := range t.recent {
		name := r.Name
		if name == "" {
			name = "Steam user " + r.ID[max(0, len(r.ID)-5):]
		}
		rv := RecentView{ID: r.ID, Name: name, At: r.At}
		if r.At > 0 {
			rv.Ago = fmtDur(now.Sub(time.Unix(r.At, 0)))
		}
		for _, tp := range t.a.srv.Tracked {
			if tp.SteamID == r.ID {
				rv.Tracked, rv.Tag, rv.Color = true, tp.Tag, tp.Color
			}
		}
		out = append(out, rv)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

// recentStatsLocked summarises the recently-played-with times, for diagnostics.
func (t *Tracker) recentStatsLocked() string {
	if len(t.recent) == 0 {
		return "none"
	}
	now := time.Now().Unix()
	var b5, b15, b60, named int
	distinct := map[int64]bool{}
	for _, r := range t.recent {
		age := now - r.At
		switch {
		case age < 300:
			b5++
		case age < 900:
			b15++
		case age < 3600:
			b60++
		}
		distinct[r.At] = true
		if r.Name != "" {
			named++
		}
	}
	return fmt.Sprintf("%d players (%d named): %d in the last 5 min, %d 5–15 min, %d 15–60 min, %d older; %d distinct times; read %s ago",
		len(t.recent), named, b5, b15, b60, len(t.recent)-b5-b15-b60, len(distinct), fmtDur(time.Since(t.recentAt)))
}
