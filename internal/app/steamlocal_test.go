package app

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"rustwatch/internal/fakerust"
)

func localTestApp(t *testing.T, addr func(q string) string) (*App, *fakerust.Server, func()) {
	fs := fakerust.New()
	fs.CensorNames = true
	if err := fs.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	qaddr, _ := fs.StartQuery("127.0.0.1:0")
	a := New(testDir(t))
	host, p, _ := net.SplitHostPort(fs.Addr)
	port, _ := strconv.Atoi(p)
	a.ConnectServer(a.AddServer(ServerConfig{IP: host, Port: port, PlayerID: "1", PlayerToken: 1, QueryHint: qaddr,
		Tracked: []TrackedPlayer{{Name: "xXSnipeKingXx", Tag: "Enemy", Notify: true}}}))
	a.DemoLocalSteam(fs.OnlineSteam, addr(qaddr))
	go a.tracker.run()
	return a, fs, func() { a.Disconnect(); time.Sleep(100 * time.Millisecond); fs.Close() }
}

func TestLocalSteamListOnCensoredServer(t *testing.T) {
	a, _, done := localTestApp(t, func(q string) string { return q })
	defer done()
	waitFor(t, "local list", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.tracker.localMode && a.tracker.status == "ok" })
	a.mu.Lock()
	v := a.tracker.viewLocked()
	sid := a.srv.Tracked[0].SteamID
	a.mu.Unlock()
	names := map[string]bool{}
	for _, o := range v.Online {
		names[o.Name] = true
		if !strings.HasPrefix(o.ID, "s:") {
			t.Fatalf("online entry not from Steam: %+v", o)
		}
	}
	// real names, not the server's stand-ins; the name-less third one is still listed
	if !names["Bob"] || !names["xXSnipeKingXx"] || names["adrian"] || len(v.Online) != len(names) || len(v.Online) < 8 {
		t.Fatalf("online %v", names)
	}
	if sid != "76561198000001002" || len(v.Tracked) != 1 || !v.Tracked[0].Online {
		t.Fatalf("auto-link: sid=%q tracked=%+v", sid, v.Tracked)
	}
	if !strings.Contains(v.LocalState, "from your Steam client") {
		t.Fatalf("state %q", v.LocalState)
	}
}

func TestLocalSteamOtherServerIgnored(t *testing.T) {
	a, _, done := localTestApp(t, func(string) string { return "203.0.113.9:28015" })
	defer done()
	waitFor(t, "poll", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.tracker.status == "ok" })
	a.mu.Lock()
	v := a.tracker.viewLocked()
	a.mu.Unlock()
	if v.LocalMode || !strings.Contains(v.LocalState, "not the server RustWatch is showing") || !v.Censored {
		t.Fatalf("localMode=%v state=%q", v.LocalMode, v.LocalState)
	}
}
