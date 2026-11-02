package app

import (
	"math"
	"math/rand"
	"time"
)

// DemoSeed fills the demo server with two weeks of history, so the stats, heatmaps, loot log,
// trails and likely teams have something to show in demo mode.
func (a *App) DemoSeed() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cfg.Servers) == 0 {
		return
	}
	sc := a.cfg.Servers[0]
	now := time.Now()
	r := rand.New(rand.NewSource(7))

	// population: quiet mornings, busy evenings, a wipe-day spike 5 days ago
	for t := now.Add(-7 * 24 * time.Hour).Truncate(10 * time.Minute); t.Before(now); t = t.Add(10 * time.Minute) {
		h := float64(t.Hour()) + float64(t.Minute())/60
		base := 70 + 85*math.Exp(-math.Pow((h-20.5)/3.2, 2)) + 25*math.Exp(-math.Pow((h-13)/3, 2))
		if d := now.Sub(t).Hours() / 24; d > 4.6 && d < 5.4 {
			base += 45
		}
		p := int32(math.Min(200, base+r.Float64()*12))
		q := int32(0)
		if p >= 195 {
			q = int32(r.Intn(9))
		}
		sc.PopLog = append(sc.PopLog, PopPoint{t.Unix(), p, 200, q})
	}

	// where the team died this wipe: mostly around the base and on the road to the outpost
	spots := [][2]float32{{1210, 2120}, {1180, 2060}, {1460, 1900}, {1500, 1860}, {1620, 1800}, {1700, 1760}, {1230, 2090}, {2300, 900}, {1480, 1880}}
	names := []string{"You", "Bob", "Alice"}
	for i, s := range spots {
		sc.DeathSpots = append(sc.DeathSpots, DeathSpot{X: s[0], Y: s[1], T: now.Add(-time.Duration(30*(i+1)) * time.Minute).Unix(), Name: names[i%3]})
	}

	// loot log on the TC
	add := func(ago time.Duration, emptied bool, ch ...LootChange) {
		for i := range ch {
			ch[i].Name, ch[i].Short = ItemName(ch[i].ID), ItemShort(ch[i].ID)
		}
		sc.LootLog = append(sc.LootLog, LootEntry{Time: now.Add(-ago).Unix(), EntityID: 3001, Box: "Main TC", Changes: ch, Emptied: emptied})
	}
	add(3*time.Hour, false, LootChange{ID: -151838493, Delta: 3000}, LootChange{ID: -2099697608, Delta: 2000})
	add(2*time.Hour, false, LootChange{ID: 69511070, Delta: 1200})
	add(70*time.Minute, false, LootChange{ID: -151838493, Delta: -400}, LootChange{ID: -2099697608, Delta: -260})
	add(25*time.Minute, false, LootChange{ID: -151838493, Delta: 1600})

	sc.Cameras = []CameraConfig{{ID: "BASECAM", Name: "Front door CCTV"}, {ID: "PTZ1", Name: "Roof PTZ"}, {ID: "TURRET1", Name: "Shooting floor turret"}, {ID: "DRONE1", Name: "Scout drone"}}
	sc.Automations = []Automation{
		{ID: "d1", Trigger: "alarm:2001", SwitchID: 1001, On: true},
		{ID: "d2", Trigger: "raid", SwitchID: 1001, On: true, RevertMin: 30},
		{ID: "d3", Trigger: "manual", SwitchID: 1001, On: true},
		{ID: "d4", Trigger: "manual", SwitchID: 1002, On: false},
	}

	// play history for the heatmaps: the enemy plays evenings, the neighbour mornings
	for i := range sc.Tracked {
		tp := &sc.Tracked[i]
		start, length := 19.0, 3.5
		switch tp.Name {
		case "Gary the Builder":
			start, length = 8, 4
		case "zerg_lord":
			start, length = 14, 6
		case "Ghost", "Hermit":
			continue
		}
		for d := 13; d >= 1; d-- {
			if r.Float64() < 0.2 {
				continue
			}
			day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -d)
			s := day.Add(time.Duration((start + r.Float64()*1.5 - 0.75) * float64(time.Hour)))
			e := s.Add(time.Duration((length + r.Float64()*2 - 1) * float64(time.Hour)))
			tp.Sessions = append(tp.Sessions, Session{Start: s.Unix(), End: e.Unix()})
		}
	}

	// join/leave history where night_raider and two others always arrive together
	var acts []Activity
	for d := 6; d >= 1; d-- {
		at := now.Add(-time.Duration(d)*24*time.Hour - 4*time.Hour).Unix()
		for k, n := range []string{"night_raider", "raid_buddy", "xXSnipeKingXx"} {
			acts = append(acts, Activity{Time: at + int64(k*12), ID: directKey(n), Name: n, Joined: true})
			acts = append(acts, Activity{Time: at + 9000 + int64(k*20), ID: directKey(n), Name: n})
		}
		for k, n := range []string{"Gary the Builder", "Gary's mate"} {
			acts = append(acts, Activity{Time: at - 30000 + int64(k*25), ID: directKey(n), Name: n, Joined: true})
		}
		for k := 0; k < 6; k++ {
			n := []string{"adrian", "aliza", "amada", "bob99", "carl", "dee"}[k]
			acts = append(acts, Activity{Time: at - int64(r.Intn(40000)), ID: directKey(n), Name: n, Joined: r.Intn(2) == 0})
		}
	}
	sortActivity(acts)
	sc.JoinLog = acts
	a.saveLocked()

	// trails from the last hour (added once connected, since connecting clears them)
	go func() {
		time.Sleep(4 * time.Second)
		a.mu.Lock()
		defer a.mu.Unlock()
		now := time.Now()
		path := func(id uint64, pts [][2]float32) {
			var tr []TrailPoint
			for i, p := range pts {
				tr = append(tr, TrailPoint{p[0], p[1], now.Add(-time.Duration(len(pts)-i) * 90 * time.Second).Unix()})
			}
			a.trails[id] = append(tr, a.trails[id]...)
		}
		var you, bob [][2]float32
		for i := 0; i < 30; i++ {
			f := float64(i) / 29
			you = append(you, [2]float32{float32(1700 - 500*f + 40*math.Sin(f*9)), float32(1760 + 340*f + 30*math.Cos(f*7))})
			a := f * 2 * math.Pi
			bob = append(bob, [2]float32{float32(1250 + 160*math.Cos(a) + 20*math.Sin(a*5)), float32(2080 + 120*math.Sin(a))})
		}
		path(76561198000000001, you)
		path(76561198000000002, bob)
	}()
}

func sortActivity(as []Activity) {
	for i := 1; i < len(as); i++ {
		for j := i; j > 0 && as[j].Time < as[j-1].Time; j-- {
			as[j], as[j-1] = as[j-1], as[j]
		}
	}
}
