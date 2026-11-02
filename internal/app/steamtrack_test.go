package app

import (
	"context"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"rustwatch/internal/fakerust"
)

func steamTestApp(t *testing.T, censor bool, hint string) (*App, func()) {
	fs := fakerust.New()
	fs.CensorNames = censor
	if err := fs.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	stats := httptest.NewServer(fs.SteamStats())
	qaddr, _ := fs.StartQuery("127.0.0.1:0")
	if hint == "" {
		hint = qaddr
	}
	a := New(testDir(t))
	a.DemoSetup(stats.URL)
	host, p, _ := net.SplitHostPort(fs.Addr)
	port, _ := strconv.Atoi(p)
	a.ConnectServer(a.AddServer(ServerConfig{IP: host, Port: port, PlayerID: "1", PlayerToken: 1, QueryHint: hint,
		Tracked: []TrackedPlayer{{SteamID: "76561198000000077", Name: "Ghost", Notify: true}, {SteamID: "76561198000000078", Name: "Hermit"}}}))
	go a.tracker.run()
	return a, func() {
		a.Disconnect()
		time.Sleep(100 * time.Millisecond)
		stats.Close()
		fs.Close()
	}
}

func trackedView(a *App, name string) TrackedView {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, tv := range a.tracker.viewLocked().Tracked {
		if tv.Name == name {
			return tv
		}
	}
	return TrackedView{}
}

// On a server that hides real names, Steam-linked players are still seen.
func TestSteamTrackingOnCensoredServer(t *testing.T) {
	a, done := steamTestApp(t, true, "")
	defer done()
	waitFor(t, "steam presence", func() bool { return trackedView(a, "Ghost").Online })
	g, h := trackedView(a, "Ghost"), trackedView(a, "Hermit")
	if g.Presence != "in Rust on this server" || h.Online || !strings.Contains(h.Presence, "private") {
		t.Fatalf("ghost=%+v hermit=%+v", g.Presence, h.Presence)
	}
	a.mu.Lock()
	v := a.tracker.viewLocked()
	a.mu.Unlock()
	if !v.Censored || !v.SteamOn {
		t.Fatalf("censored=%v steamOn=%v", v.Censored, v.SteamOn)
	}
	found := false
	for _, o := range v.Online {
		if o.Name == "Ghost" && o.Tracked {
			found = true
		}
	}
	if !found {
		t.Fatal("Ghost missing from the online list")
	}
	if s := a.tracker.chatSummaryLocked(); !strings.Contains(s, "Tracked online: Ghost") {
		t.Fatalf("chat summary %q", s)
	}
}

// When the server won't give a player list at all, Steam-linked players still work.
func TestSteamTrackingWithoutPlayerList(t *testing.T) {
	a, done := steamTestApp(t, false, "127.0.0.1:1")
	defer done()
	waitFor(t, "steam presence", func() bool { return trackedView(a, "Ghost").Online })
	a.mu.Lock()
	st := a.tracker.status
	a.mu.Unlock()
	if st == "ok" {
		t.Fatal("status should still report the player-list problem")
	}
}

func TestTrackBySteamLink(t *testing.T) {
	a, done := steamTestApp(t, false, "")
	defer done()
	if err := a.Track(TrackedPlayer{SteamID: "https://steamcommunity.com/id/ghost/", Tag: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Track(TrackedPlayer{SteamID: "https://steamcommunity.com/profiles/76561198000001234"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Track(TrackedPlayer{SteamID: "https://steamcommunity.com/id/nobody"}); err == nil {
		t.Fatal("unknown vanity should fail")
	}
	a.mu.Lock()
	n := len(a.srv.Tracked)
	ghost := a.srv.Tracked[0]
	last := a.srv.Tracked[n-1]
	a.mu.Unlock()
	if n != 3 || ghost.Tag != "x" || last.SteamID != "76561198000001234" || last.Name != "Player 1234" {
		t.Fatalf("n=%d ghost=%+v last=%+v", n, ghost, last)
	}
	a.Untrack("", "", "76561198000001234")
	a.mu.Lock()
	n = len(a.srv.Tracked)
	a.mu.Unlock()
	if n != 2 {
		t.Fatalf("untrack left %d", n)
	}
}

func TestSteamWhere(t *testing.T) {
	tr := &Tracker{herePorts: map[int]bool{}, portChecked: map[int]bool{}}
	sc := &ServerConfig{IP: "1.2.3.4", Port: 28082, QueryAddr: "1.2.3.4:28016"}
	sa := tr.addrsLocked(sc, "x")
	sa.resolve(context.Background())
	for gs, want := range map[string]bool{"1.2.3.4:28015": true, "1.2.3.4:28016": true, "5.6.7.8:28015": false, "0.0.0.0:0": false} {
		if here, _ := tr.where(sa, gs); here != want {
			t.Errorf("%s: got %v", gs, here)
		}
	}
	if here, verify := tr.where(sa, "1.2.3.4:29015"); here || verify != 29015 {
		t.Errorf("other port on same host: here=%v verify=%d", here, verify)
	}
	if !looksLikeSteam("https://steamcommunity.com/id/abc") || !looksLikeSteam("76561198000000077") || looksLikeSteam("1234567") {
		t.Error("looksLikeSteam")
	}
}
