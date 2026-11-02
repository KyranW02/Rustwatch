package app

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SaveDiagnostics writes a text file with what RustWatch currently sees from the server, including
// the raw map-marker reply, so problems like "no shops" can be investigated. No tokens or keys.
func (a *App) SaveDiagnostics() (string, error) {
	a.mu.Lock()
	var b strings.Builder
	fmt.Fprintf(&b, "RustWatch %s diagnostics, %s\n\n", Version, time.Now().Format(time.RFC1123))
	fmt.Fprintf(&b, "connection: %s %s\n", a.status, a.statusErr)
	if a.srv != nil {
		fmt.Fprintf(&b, "server: %s (proxy %v)\n", a.srv.Name, a.srv.UseProxy)
	}
	if a.info != nil {
		fmt.Fprintf(&b, "map: %s, size %d, players %d/%d\n", a.info.Map, a.info.MapSize, a.info.Players, a.info.MaxPlayers)
	}
	if a.mapData != nil {
		fmt.Fprintf(&b, "map image %dx%d, ocean margin %d, %d monuments\n", a.mapData.Width, a.mapData.Height, a.mapData.OceanMargin, len(a.mapData.Monuments))
	}
	d := a.markerDiag
	tr := a.tracker
	fmt.Fprintf(&b, "\nplayer tracker: source %s, status %q, %d online, shown %d of %d, censored %v\n", tr.source, tr.status, len(tr.online), tr.shown, tr.total, tr.censored)
	fmt.Fprintf(&b, "in-game Steam list: in use %v, %q (server Steam ID %d)\n", tr.localMode, tr.localState, tr.serverSteamID)
	fmt.Fprintf(&b, "Steam recently played with (Rust): %s\n", tr.recentStatsLocked())
	fmt.Fprintf(&b, "\nmap markers: %d total, %d shops\n", d.Total, d.Vending)
	var types []int
	for t := range d.ByType {
		types = append(types, int(t))
	}
	sort.Ints(types)
	for _, t := range types {
		fmt.Fprintf(&b, "  type %d: %d\n", t, d.ByType[int32(t)])
	}
	if d.At > 0 {
		fmt.Fprintf(&b, "last good fetch: %s\n", time.Unix(d.At, 0).Format("15:04:05"))
	}
	if d.Error != "" {
		fmt.Fprintf(&b, "last error: %s at %s\n", d.Error, time.Unix(d.ErrAt, 0).Format("15:04:05"))
	}
	if a.vending != nil {
		fmt.Fprintf(&b, "shops RustWatch is tracking: %d\n", len(a.vending.machines))
	}
	fmt.Fprintf(&b, "team members: %d\n", len(a.teamViewLocked()))
	raw := d.raw
	a.mu.Unlock()

	fmt.Fprintf(&b, "\nraw map-marker reply (%d bytes, base64):\n", len(raw))
	enc := base64.StdEncoding.EncodeToString(raw)
	if len(enc) > 400_000 {
		enc = enc[:400_000] + "…(truncated)"
	}
	for len(enc) > 0 {
		n := min(100, len(enc))
		b.WriteString(enc[:n] + "\n")
		enc = enc[n:]
	}
	dir := filepath.Join(a.dir, "logs")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "diagnostics-"+time.Now().Format("2006-01-02-150405")+".txt")
	return path, os.WriteFile(path, []byte(b.String()), 0o644)
}
