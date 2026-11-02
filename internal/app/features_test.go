package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rustwatch/internal/fakerust"
	"rustwatch/internal/rustplus"
)

// connected starts a fake server and an app connected to it.
func connected(t *testing.T, devs []Device) (*App, *fakerust.Server) {
	t.Helper()
	fs := fakerust.New()
	if err := fs.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fs.Close)
	a := New(testDir(t))
	host, p, _ := net.SplitHostPort(fs.Addr)
	port, _ := strconv.Atoi(p)
	id := a.AddServer(ServerConfig{IP: host, Port: port, PlayerID: "76561198000000001", PlayerToken: 1, Devices: devs})
	a.ConnectServer(id)
	waitFor(t, "connected", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.status == "connected" })
	return a, fs
}

func TestCameraStream(t *testing.T) {
	a, _ := connected(t, nil)
	if err := a.SaveCamera("PTZ1", "Roof"); err != nil {
		t.Fatal(err)
	}
	if err := a.cams.Open("PTZ1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "camera frames", func() bool {
		a.mu.Lock()
		v := a.cams.viewLocked(a.srv.Cameras)
		a.mu.Unlock()
		return v.Active != nil && v.Active.Frames >= 8 && v.Active.Width == 160
	})
	png1, err := a.cams.PNG()
	if err != nil || len(png1) < 500 {
		t.Fatalf("png %d %v", len(png1), err)
	}
	a.mu.Lock()
	v := a.cams.viewLocked(a.srv.Cameras)
	a.mu.Unlock()
	if v.Active.Name != "Roof" || v.Active.Controls != rustplus.CamMouse || v.Active.Error != "" {
		t.Errorf("view %+v", v.Active)
	}
	// look around: the picture changes
	if err := a.cams.Input(0, 120, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	png2, _ := a.cams.PNG()
	if string(png1) == string(png2) {
		t.Error("picture didn't change after turning the camera")
	}
	// a wrong ID explains itself
	a.cams.Open("NOPE")
	waitFor(t, "camera error", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		v := a.cams.viewLocked(a.srv.Cameras)
		return v.Active != nil && strings.Contains(v.Active.Error, "No camera with that ID")
	})
	a.cams.Close()
}

func TestTurretCameraSeesPlayer(t *testing.T) {
	a, _ := connected(t, nil)
	a.cams.Open("TURRET1")
	waitFor(t, "player in view", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		v := a.cams.viewLocked(nil)
		return v.Active != nil && len(v.Active.Players) == 1 && v.Active.Players[0] == "night_raider" && v.Active.Controls&rustplus.CamFire != 0
	})
	a.cams.Close()
}

func TestAlarmAutomation(t *testing.T) {
	a, _ := connected(t, []Device{{EntityID: 1001, Type: 1, Name: "Turrets"}, {EntityID: 1002, Type: 1, Name: "Lights"}, {EntityID: 2001, Type: 2, Name: "Base alarm"}})
	if err := a.SaveAutomation(Automation{Trigger: "alarm:2001", SwitchID: 1001, On: true}); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveAutomation(Automation{Trigger: "manual", SwitchID: 1002, On: false}); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveAutomation(Automation{Trigger: "alarm:any", SwitchID: 9999, On: true}); err == nil {
		t.Error("accepted an unknown switch")
	}
	state := func(id uint32) bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		ds := a.devState[id]
		return ds != nil && ds.Payload != nil && ds.Payload.Value
	}
	if state(1001) {
		t.Fatal("turrets start off")
	}
	// the alarm goes off
	a.onBroadcast(&rustplus.Broadcast{EntityChanged: &rustplus.EntityChanged{EntityID: 2001, Payload: rustplus.Payload{HasValue: true, Value: true}}})
	waitFor(t, "turrets switched on by the alarm", func() bool { return state(1001) })
	// raid mode turns the lights off; raid mode off turns them back on
	if n, err := a.RaidMode(true); err != nil || n != 1 {
		t.Fatalf("raid mode %d %v", n, err)
	}
	waitFor(t, "lights off", func() bool { return !state(1002) })
	a.RaidMode(false)
	waitFor(t, "lights back on", func() bool { return state(1002) })
	a.mu.Lock()
	views := a.automationsViewLocked()
	a.mu.Unlock()
	reply := a.Command(0, "raidmode")
	if !strings.Contains(views[0].Text, "When alarm 'Base alarm' goes off, turn Turrets on") {
		t.Errorf("text %q", views[0].Text)
	}
	if !strings.Contains(reply, "Raid mode on:") {
		t.Errorf("reply %q", reply)
	}
}

func TestLootLog(t *testing.T) {
	a := New(testDir(t))
	a.mu.Lock()
	defer a.mu.Unlock()
	a.srv = &ServerConfig{Name: "x"}
	box := Device{EntityID: 7, Type: rustplus.EntityStorageMonitor, Name: "Loot room"}
	full := &rustplus.Payload{Capacity: 48, Items: []rustplus.Item{{ItemID: -932201673, Quantity: 5000}, {ItemID: 69511070, Quantity: 2000}, {ItemID: 317398316, Quantity: 300}, {ItemID: 1545779598, Quantity: 1}}}
	more := &rustplus.Payload{Capacity: 48, Items: append(append([]rustplus.Item{}, full.Items...), rustplus.Item{ItemID: -932201673, Quantity: 1000})}
	a.recordLootLocked(box, full, more)
	if n := len(a.srv.LootLog); n != 1 || a.srv.LootLog[0].Changes[0].Delta != 1000 || a.srv.LootLog[0].Emptied {
		t.Fatalf("log %+v", a.srv.LootLog)
	}
	empty := &rustplus.Payload{Capacity: 48, Items: []rustplus.Item{}}
	a.recordLootLocked(box, more, empty)
	last := a.srv.LootLog[len(a.srv.LootLog)-1]
	if !last.Emptied || len(a.logs) == 0 || a.logs[len(a.logs)-1].Kind != "box_emptied" {
		t.Fatalf("emptied not flagged: %+v %+v", last, a.logs)
	}
	if !strings.Contains(a.logs[len(a.logs)-1].Text, "6k Scrap") {
		t.Errorf("alert text %q", a.logs[len(a.logs)-1].Text)
	}
	// partial readings (no capacity) aren't diffed
	a.recordLootLocked(box, empty, &rustplus.Payload{})
	if len(a.srv.LootLog) != 2 {
		t.Error("partial reading logged")
	}
	if s := a.lootSummaryLocked(); !strings.Contains(s, "Loot room") || !strings.Contains(s, "-6k Scrap") {
		t.Errorf("summary %q", s)
	}
}

func TestClanOfflineAndGroups(t *testing.T) {
	a := New(testDir(t))
	a.mu.Lock()
	defer a.mu.Unlock()
	tr := a.tracker
	tr.source = "direct"
	tr.first = false
	sc := &ServerConfig{Tracked: []TrackedPlayer{{Name: "alpha", Tag: "Enemy", Notify: true}, {Name: "bravo", Tag: "enemy"}, {Name: "solo", Tag: "neighbour"}}}
	a.srv = sc
	cur := map[string]seenPlayer{directKey("alpha"): {Name: "alpha"}, directKey("bravo"): {Name: "bravo"}, directKey("solo"): {Name: "solo"}}
	tr.apply(sc, cur)
	delete(cur, directKey("alpha"))
	tr.apply(sc, cur)
	for _, l := range a.logs {
		if l.Kind == "clan_offline" {
			t.Fatal("alerted while bravo is still on")
		}
	}
	delete(cur, directKey("bravo"))
	delete(cur, directKey("solo"))
	tr.apply(sc, cur)
	var got []string
	for _, l := range a.logs {
		if l.Kind == "clan_offline" {
			got = append(got, l.Text)
		}
	}
	if len(got) != 1 || !strings.Contains(got[0], "All 2 players tagged [Enemy]") {
		t.Errorf("clan alerts %q", got)
	}

	// likely groups: x and y join and leave together 3 times, z is random
	tr.activity = nil
	base := time.Now().Add(-5 * time.Hour).Unix()
	for k := int64(0); k < 3; k++ {
		at := base + k*3600
		tr.activity = append(tr.activity, Activity{Time: at, ID: "x", Name: "X", Joined: true}, Activity{Time: at + 20, ID: "y", Name: "Y", Joined: true},
			Activity{Time: at + 1500, ID: "x", Name: "X"}, Activity{Time: at + 1510, ID: "y", Name: "Y"}, Activity{Time: at + 900*(k+1), ID: "z", Name: "Z", Joined: true})
	}
	tr.groups = nil
	gs := tr.likelyGroupsLocked(map[string]bool{"y": true})
	if len(gs) != 1 || len(gs[0].Members) != 2 || gs[0].Members[0].Name != "X" || !gs[0].Members[1].Tracked || gs[0].Together < 3 {
		t.Errorf("groups %+v", gs)
	}
}

func TestForcedWipe(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/London")
	cases := map[string]string{
		"2026-11-02 10:00": "2026-11-05 19:00", // first Thursday of November, UK winter time
		"2026-11-05 19:30": "2026-12-03 19:00",
		"2026-06-30 12:00": "2026-07-02 19:00", // summer: 19:00 BST
	}
	for now, want := range cases {
		n, _ := time.ParseInLocation("2006-01-02 15:04", now, loc)
		if got := NextForcedWipe(n).In(loc).Format("2006-01-02 15:04"); got != want {
			t.Errorf("%s -> %s, want %s", now, got, want)
		}
	}
}

func TestPopStats(t *testing.T) {
	a := New(testDir(t))
	a.mu.Lock()
	a.srv = &ServerConfig{}
	a.info = &rustplus.Info{Players: 100, MaxPlayers: 200}
	start := time.Now().Add(-48 * time.Hour).Truncate(time.Hour)
	for i := 0; i < 48*6; i++ {
		at := start.Add(time.Duration(i) * 10 * time.Minute)
		a.info.Players = uint32(50 + 100*boolInt(at.Hour() >= 18 && at.Hour() < 23))
		a.recordPopLocked(at)
	}
	a.recordPopLocked(start.Add(48*time.Hour - 5*time.Minute)) // too soon after the last: skipped
	a.mu.Unlock()
	s := a.Stats()
	if len(s.Points) != 48*6 {
		t.Fatalf("points %d", len(s.Points))
	}
	if len(s.Busy) != 3 || s.Busy[0] < 18 || s.Busy[0] > 22 || s.Peak.P != 150 {
		t.Errorf("busy %v peak %+v", s.Busy, s.Peak)
	}
	if s.Forced == 0 || s.ForcedIn == "" {
		t.Error("no forced wipe")
	}
}

func boolInt(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

func TestRaidCost(t *testing.T) {
	if s := raidCostLine("stone wall", "!"); !strings.Contains(s, "2 C4 (4,400 sulfur), 4 rockets (5,600)") {
		t.Error(s)
	}
	if s := raidCostLine("armored door 2", "!"); !strings.Contains(s, "2× Armored door: 6 C4 (13,200 sulfur)") {
		t.Error(s)
	}
	if s := raidCostLine("hqm wall", "!"); !strings.HasPrefix(s, "Armored wall") {
		t.Error(s)
	}
	if s := raidCostLine("sofa", "!"); !strings.HasPrefix(s, "Usage") {
		t.Error(s)
	}
}

func TestTrails(t *testing.T) {
	a := New(testDir(t))
	a.mu.Lock()
	defer a.mu.Unlock()
	a.srv = &ServerConfig{}
	now := time.Now()
	m := rustplus.Member{SteamID: 1, Name: "Bob", Online: true, Alive: true}
	for i := 0; i < 10; i++ {
		m.X, m.Y = float32(100+i*20), 500
		a.recordTrailLocked(m, now.Add(time.Duration(i)*time.Second))
	}
	m.X = 105 // too close to the last point? no: moved back 275 m
	a.recordDeathLocked(m, 280, 500, now.Add(11*time.Second))
	a.team = &rustplus.TeamInfo{Members: []rustplus.Member{m}}
	v := a.trailsViewLocked()
	if len(v) != 1 || len(v[0].Points) != 11 || v[0].Points[10] != [2]float32{-1, -1} {
		t.Errorf("trail %+v", v)
	}
	if len(a.srv.DeathSpots) != 1 || a.srv.DeathSpots[0].Name != "Bob" {
		t.Errorf("deaths %+v", a.srv.DeathSpots)
	}
}

// fake Discord API: one channel, records what the bot posts
type fakeDiscord struct {
	mu     sync.Mutex
	msgs   []map[string]any
	posted []string
}

func (f *fakeDiscord) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bot tok" {
		w.WriteHeader(401)
		return
	}
	switch {
	case r.URL.Path == "/users/@me":
		fmt.Fprint(w, `{"id":"99","username":"RustWatchBot"}`)
	case strings.HasSuffix(r.URL.Path, "/messages") && r.Method == "GET":
		after, _ := strconv.Atoi(r.URL.Query().Get("after"))
		var out []map[string]any
		for i := len(f.msgs) - 1; i >= 0; i-- { // newest first, like Discord
			id, _ := strconv.Atoi(f.msgs[i]["id"].(string))
			if r.URL.Query().Get("limit") == "1" || id > after {
				out = append(out, f.msgs[i])
				if r.URL.Query().Get("limit") == "1" {
					break
				}
			}
		}
		json.NewEncoder(w).Encode(out)
	case strings.HasSuffix(r.URL.Path, "/messages") && r.Method == "POST":
		var b struct {
			Content string `json:"content"`
		}
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &b)
		f.posted = append(f.posted, b.Content)
		fmt.Fprint(w, `{}`)
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeDiscord) add(id int, user, text string, bot bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, map[string]any{"id": fmt.Sprint(id), "content": text, "author": map[string]any{"id": user, "username": user, "bot": bot}})
}

func TestDiscordTwoWay(t *testing.T) {
	a, _ := connected(t, []Device{{EntityID: 1001, Type: 1, Name: "Turrets", Command: "turrets"}})
	fd := &fakeDiscord{}
	fd.add(1, "kyran", "old message", false)
	srv := httptest.NewServer(fd)
	defer srv.Close()
	a.discord.base = srv.URL
	a.mu.Lock()
	a.cfg.Settings.DiscordBotToken, a.cfg.Settings.DiscordChannel = "tok", "123"
	a.cfg.Settings.DiscordRelayChat, a.cfg.Settings.DiscordToTeam = true, true
	a.mu.Unlock()
	go a.discord.run()
	waitFor(t, "discord connected", func() bool { return a.discord.Status() == "connected as RustWatchBot" })
	fd.add(2, "kyran", "!turrets on", false)
	fd.add(3, "otherbot", "!pop", true)
	fd.add(4, "kyran", "omw to base", false)
	waitFor(t, "command reply", func() bool {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		return len(fd.posted) >= 1
	})
	fd.mu.Lock()
	first := fd.posted[0]
	fd.mu.Unlock()
	if first != "Turrets turned ON." {
		t.Errorf("reply %q", first)
	}
	waitFor(t, "discord message in team chat", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, c := range a.chat {
			if strings.Contains(c.Message, "[Discord] kyran: omw to base") {
				return true
			}
		}
		return false
	})
	// team chat goes to Discord
	a.onTeamMessage(rustplus.TeamMessage{SteamID: 2, Name: "Bob", Message: "heli at dome"})
	waitFor(t, "relayed chat", func() bool {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		for _, p := range fd.posted {
			if p == "**Bob**: heli at dome" {
				return true
			}
		}
		return false
	})
	fd.mu.Lock()
	for _, p := range fd.posted {
		if strings.Contains(p, "old message") || strings.Contains(p, "Population") {
			t.Errorf("replayed or answered a bot: %q", p)
		}
	}
	fd.mu.Unlock()
	// a bad token shows up as the status
	a.mu.Lock()
	a.cfg.Settings.DiscordBotToken = "bad"
	a.mu.Unlock()
	waitFor(t, "bad token status", func() bool { return strings.Contains(a.discord.Status(), "rejected the bot token") })
}
