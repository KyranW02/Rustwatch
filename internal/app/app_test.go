package app

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"rustwatch/internal/fakerust"
	"rustwatch/internal/rustplus"
)

func TestItemSearch(t *testing.T) {
	cases := map[string]string{
		"ak": "Assault Rifle", "tc": "Tool Cupboard", "level 3 workbench": "Workbench Level 3", "c4": "Timed Explosive Charge",
		"thompson": "Thompson", "hqm": "High Quality Metal", "assualt rifle": "Assault Rifle", "rifle.ak": "Assault Rifle",
		"road sign vest": "Road Sign Jacket", "scrap": "Scrap", "tactical gloves": "Tactical Gloves",
	}
	for q, want := range cases {
		it, ok := FindItem(q)
		if !ok || it.Name != want {
			t.Errorf("%q -> %q, want %q", q, it.Name, want)
		}
	}
	it, _ := FindItem("ak")
	d, _ := GetItemDetail(it.ID)
	if d.Craft == nil || d.Craft.Workbench != "Workbench Level 3" || len(d.Recycle) == 0 {
		t.Errorf("ak detail %+v", d)
	}
}

func TestGrid(t *testing.T) {
	if g := Grid(0, 3500, 3500); g != "A0" {
		t.Errorf("top-left %s", g)
	}
	if g := Grid(150, 3500-150, 3500); g != "B1" {
		t.Errorf("b1 %s", g)
	}
}

func TestEventEngine(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e := NewEventEngine()
	e.Now = func() time.Time { return now }
	e.MapSize = 3500
	e.Monuments = []rustplus.Monument{{Token: "oil_rig_small", X: 3300, Y: 1800}, {Token: "launchsite", X: 2300, Y: 900}, {Token: "harbor_display_name", X: 1000, Y: 450}}
	var got []string
	e.Emit = func(k, title, text string) { got = append(got, title) }
	e.Update(nil) // baseline
	now = now.Add(10 * time.Second)
	e.Update([]rustplus.Marker{{ID: 1, Type: 5, X: -200, Y: 1200}, {ID: 2, Type: 8, X: 1700, Y: 1700}, {ID: 3, Type: 4, X: 3250, Y: 1800}})
	now = now.Add(10 * time.Second)
	e.Update([]rustplus.Marker{{ID: 1, Type: 5, X: 1000, Y: 470}, {ID: 4, Type: 2, X: 2310, Y: 910}})
	if e.BradleyDown.IsZero() {
		t.Error("bradley not detected")
	}
	now = now.Add(16 * time.Minute)
	e.Tick()
	now = now.Add(time.Minute)
	e.Update(nil)
	want := []string{"Cargo ship spawned", "Patrol helicopter spawned", "Small Oil Rig triggered", "Cargo ship docked", "Bradley destroyed", "Patrol heli taken down", "Small Oil Rig crate unlocked", "Cargo ship left"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("events:\n got %v\nwant %v", got, want)
	}
	if !strings.Contains(e.SmallOilStatus(), "unlocked") {
		t.Error(e.SmallOilStatus())
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"15s": 15 * time.Second, "20m": 20 * time.Minute, "3h": 3 * time.Hour, "1h30m": 90 * time.Minute, "5": 5 * time.Minute} {
		if d, err := parseDuration(in); err != nil || d != want {
			t.Errorf("%s -> %v %v", in, d, err)
		}
	}
	if _, err := parseDuration("abc"); err == nil {
		t.Error("expected error")
	}
}

func TestSplitChat(t *testing.T) {
	parts := splitChat(strings.Repeat("word ", 60), 120)
	for _, p := range parts {
		if len(p) > 120 {
			t.Fatalf("chunk too long %d", len(p))
		}
	}
	if len(parts) < 2 {
		t.Fatal("expected split")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestEndToEnd(t *testing.T) {
	fs := fakerust.New()
	if err := fs.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	bm := httptest.NewServer(fs.BattleMetrics())
	defer bm.Close()

	stats := httptest.NewServer(fs.SteamStats())
	defer stats.Close()
	qaddr, err := fs.StartQuery("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := New(testDir(t))
	a.SetBMBaseURL(bm.URL)
	a.DemoSetup(stats.URL)
	host, p, _ := net.SplitHostPort(fs.Addr)
	port, _ := strconv.Atoi(p)
	id := a.AddServer(ServerConfig{IP: host, Port: port, PlayerID: "76561198000000001", PlayerToken: 1, QueryHint: qaddr,
		Devices: []Device{{EntityID: 1001, Type: 1, Name: "Turrets", Command: "turrets"}, {EntityID: 3001, Type: 3, Name: "TC"}},
		Tracked: []TrackedPlayer{{BMID: "1002", Name: "xXSnipeKingXx", Tag: "Enemy", Notify: true}, {Name: "zerg_lord", Notify: true}}})
	a.ConnectServer(id)
	go a.tracker.run()
	go a.statsLoop()

	waitFor(t, "connected", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.status == "connected" })
	a.mu.Lock()
	st := a.stateLocked()
	a.mu.Unlock()
	if st.Info == nil || st.Info.Players != 147 || len(st.Team) != 3 || len(st.Vending) != 14 || st.Time == nil || st.Map == nil {
		t.Fatalf("state incomplete: info=%v team=%d vend=%d", st.Info, len(st.Team), len(st.Vending))
	}
	if len(st.Devices) != 2 || st.Devices[1].UpkeepLeft == "" || len(st.Devices[1].Items) != 3 {
		t.Fatalf("devices %+v", st.Devices)
	}

	me := uint64(76561198000000001)
	checks := map[string]string{
		"pop":          "Population: 147/200, 3 in queue",
		"buy ak":       "Selling Assault Rifle: L12: 1x for 450 Scrap (2 left)",
		"sell scrap":   "Buying Scrap",
		"craft ak":     "Assault Rifle: 50x High Quality Metal",
		"recycle ak":   "Safe-zone recycler",
		"grecycle ak":  "Monument recycler",
		"team":         "Online: Bob, You | Offline: Alice",
		"switches":     "!turrets (Turrets): OFF",
		"time":         "In-game time is",
		"wipe":         "Wiped 1d 2h ago",
		"timer 10m go": "Timer set: 'go' in 10m 0s.",
		"upkeep":       "Upkeep: TC: 1d",
		"help":         "Commands:",
		"oil":          "Small Oil Rig: not triggered",
	}
	for cmd, want := range checks {
		if got := a.Command(me, cmd); !strings.Contains(got, want) {
			t.Errorf("!%s -> %q, want contains %q", cmd, got, want)
		}
	}
	if got := a.Command(me, "turrets"); got != "Turrets turned ON." {
		t.Errorf("toggle: %q", got)
	}
	waitFor(t, "switch broadcast", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		ds := a.devState[1001]
		return ds != nil && ds.Payload != nil && ds.Payload.Value
	})

	// chat command round trip: a teammate types !pop, the bot replies in team chat
	a.onTeamMessage(rustplus.TeamMessage{SteamID: 76561198000000002, Name: "Bob", Message: "!pop", Time: uint32(time.Now().Unix())})
	waitFor(t, "bot reply", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, c := range a.chat {
			if strings.HasPrefix(c.Message, "[RW] Population") && c.Bot {
				return true
			}
		}
		return false
	})

	// team online alert from the simulated server (Alice comes online after ~15s)
	// player tracker, free direct server query (default)
	waitFor(t, "direct tracker poll", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.tracker.status == "ok" })
	a.mu.Lock()
	tv := a.tracker.viewLocked()
	if tv.Source != "direct" || tv.QueryAddr != qaddr || len(tv.Online) < 8 || !tv.Tracked[0].Online || tv.Queue != 3 {
		t.Errorf("direct tracker %+v (status %s)", tv, a.tracker.status)
	}
	for _, o := range tv.Online {
		if o.Name == "Mr. Roof Camper" && (!o.Known || !strings.HasPrefix(o.OnlineFor, "3h")) {
			t.Errorf("session length from query: %+v", o)
		}
	}
	if !strings.Contains(a.tracker.chatSummaryLocked(), "[Enemy] xXSnipeKingXx") {
		t.Error(a.tracker.chatSummaryLocked())
	}
	a.cfg.Settings.BMToken = "paid-key"
	a.mu.Unlock()

	// with a BattleMetrics key it switches to BattleMetrics
	a.tracker.poke()
	waitFor(t, "bm tracker poll", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.tracker.source == "battlemetrics" && a.tracker.status == "ok"
	})
	a.mu.Lock()
	tv = a.tracker.viewLocked()
	if tv.ServerID != "424242" || !tv.Tracked[0].Online || a.srv.BMServerID != "424242" {
		t.Errorf("bm tracker %+v", tv)
	}

	// kills from Steam stats
	a.mu.Unlock()
	waitFor(t, "steam stats", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		done := 0
		for _, m := range a.teamViewLocked() {
			if m.HasStats || m.StatsError != "" {
				done++
			}
		}
		return done == 3
	})
	a.mu.Lock()
	for _, m := range a.teamViewLocked() {
		switch m.Name {
		case "You":
			if m.Kills < 1843 || m.KD == "" || m.Hours != 2539 || m.WipeKills != 0 {
				t.Errorf("stats %+v", m)
			}
		case "Alice":
			if m.HasStats || !strings.Contains(m.StatsError, "private") {
				t.Errorf("private stats %+v", m)
			}
		}
	}
	a.mu.Unlock()

	// HTTP layer
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/state", nil)
	req.Host = "127.0.0.1"
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("state http %v %v", err, resp.Status)
	}
	req2, _ := http.NewRequest("GET", srv.URL+"/api/state", nil)
	req2.Host = "evil.example"
	resp2, _ := http.DefaultClient.Do(req2)
	if resp2.StatusCode != 403 {
		t.Errorf("expected rebinding protection, got %d", resp2.StatusCode)
	}
	a.Disconnect()
}

// testDir is a temp data folder removed after the app's background saver has settled.
func testDir(t *testing.T) string {
	d, err := os.MkdirTemp("", "rwtest")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { time.Sleep(200 * time.Millisecond); os.RemoveAll(d) })
	return d
}
