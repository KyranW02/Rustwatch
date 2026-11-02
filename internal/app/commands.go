package app

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"rustwatch/internal/rustplus"
)

var builtinCommands = map[string]bool{
	"pop": true, "time": true, "wipe": true, "team": true, "cargo": true, "heli": true, "chinook": true, "ch47": true,
	"crate": true, "oil": true, "smoil": true, "small": true, "loil": true, "large": true, "bradley": true, "brad": true,
	"vendor": true, "buy": true, "sell": true, "craft": true, "recycle": true, "grecycle": true, "switch": true,
	"switches": true, "promote": true, "leader": true, "tracked": true, "timer": true, "timers": true, "help": true,
	"version": true, "notification": true, "next": true, "spawns": true, "online": true, "offline": true, "afk": true, "events": true, "upkeep": true,
	"excavator": true, "exca": true, "deepsea": true, "ds": true,
}

func (a *App) runCommand(m rustplus.TeamMessage, line string) {
	reply := a.Command(m.SteamID, line)
	if reply != "" {
		a.SendChat(reply)
	}
}

// Command executes a chat command and returns the reply text (exported for tests / UI console).
func (a *App) Command(sender uint64, line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	cmd := strings.ToLower(fields[0])
	args := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.srv == nil {
		return ""
	}
	ev := a.events
	switch cmd {
	case "help":
		p := a.cfg.Settings.Prefix
		return fmt.Sprintf("Commands: %[1]spop %[1]stime %[1]swipe %[1]steam %[1]scargo %[1]sheli %[1]schinook %[1]soil %[1]sbradley %[1]svendor %[1]snext %[1]sbuy <item> %[1]ssell <item> %[1]scraft <item> %[1]srecycle <item> %[1]sgrecycle <item> %[1]sswitch <id> <name> %[1]sswitches %[1]s<switch> [on|off] %[1]spromote <name> %[1]stracked %[1]stimer <10m> <msg> %[1]supkeep %[1]sversion", p)
	case "version":
		return "RustWatch " + Version
	case "notification":
		a.alertLockedForce("Test notification", "RustWatch notifications are working")
		return "Sent a test notification to the desktop app."
	case "pop":
		if a.info == nil {
			return "Server info not loaded yet."
		}
		i := a.info
		s := fmt.Sprintf("Population: %d/%d", i.Players, i.MaxPlayers)
		if i.QueuedPlayers > 0 {
			s += fmt.Sprintf(", %d in queue", i.QueuedPlayers)
		}
		if !a.tracker.lastPoll.IsZero() {
			s += fmt.Sprintf(", %d in the live player list", a.tracker.onlineCount())
		}
		return s
	case "time":
		tv := a.timeViewLocked()
		if tv == nil {
			return "Time not loaded yet."
		}
		return fmt.Sprintf("In-game time is %s, %s %s.", tv.Clock, tv.Until, tv.UntilLabel)
	case "wipe":
		if a.info == nil || a.info.WipeTime == 0 {
			return "Wipe time unknown."
		}
		w := time.Unix(int64(a.info.WipeTime), 0)
		return fmt.Sprintf("Wiped %s ago (%s).", fmtDur(time.Since(w)), w.Format("Mon 2 Jan 15:04"))
	case "team", "online", "offline", "afk":
		tv := a.teamViewLocked()
		if len(tv) == 0 {
			return "Team info not loaded."
		}
		var on, off, afk []string
		for _, m := range tv {
			switch {
			case m.Online && m.AFK:
				afk = append(afk, fmt.Sprintf("%s (%s)", m.Name, m.AFKFor))
			case m.Online:
				on = append(on, m.Name)
			default:
				off = append(off, m.Name)
			}
		}
		parts := []string{"Online: " + listOr(on)}
		if len(afk) > 0 {
			parts = append(parts, "AFK: "+strings.Join(afk, ", "))
		}
		parts = append(parts, "Offline: "+listOr(off))
		return strings.Join(parts, " | ")
	case "cargo":
		if a.markersCutLocked() && ev.CargoHeard.IsZero() {
			return "Cargo isn't on Rust+ any more and hasn't been heard yet (teach its horn in RustWatch → Sounds). " + a.nextLine("cargo")
		}
		return ev.CargoStatus() + " " + a.nextLine("cargo")
	case "heli":
		if a.markersCutLocked() && ev.HeliDown.IsZero() {
			return "Patrol heli isn't on Rust+ any more (Rust's Aug 2026 update). " + a.nextLine("heli")
		}
		return ev.HeliStatus() + " " + a.nextLine("heli")
	case "chinook", "ch47", "crate":
		return ev.ChinookStatus() + " " + a.nextLine("chinook")
	case "next", "spawns":
		var parts []string
		for _, f := range a.forecastsLocked() {
			parts = append(parts, f.Label+": "+f.Short)
		}
		return strings.Join(parts, " | ")
	case "oil":
		return ev.SmallOilStatus() + " " + ev.LargeOilStatus()
	case "smoil", "small":
		return ev.SmallOilStatus()
	case "loil", "large":
		return ev.LargeOilStatus()
	case "bradley", "brad":
		return ev.BradleyStatus()
	case "vendor":
		if a.markersCutLocked() && ev.VendorHeard.IsZero() {
			return "The travelling vendor isn't on Rust+ any more and hasn't been heard nearby yet. " + a.nextLine("vendor")
		}
		return ev.VendorStatus() + " " + a.nextLine("vendor")
	case "excavator", "exca":
		return ev.ExcavatorStatus()
	case "deepsea", "ds":
		return ev.DeepSeaStatus()
	case "events":
		s := ev.Summary()
		return strings.Join([]string{s.Cargo, s.Heli, s.Chinook, s.Small, s.Large, s.Bradley, s.Vendor, s.Excavator, s.DeepSea}, "\n")
	case "buy", "sell":
		if args == "" {
			return "Usage: " + a.cfg.Settings.Prefix + cmd + " <item>"
		}
		it, ok := FindItem(args)
		if !ok {
			return "Unknown item: " + args
		}
		if a.markersCutLocked() {
			return "Shops can't be searched: Rust stopped sending vending machines to Rust+ in its Aug 2026 update."
		}
		return vendingChatSummary(a.vending.search(it.ID, cmd == "buy"), it.ID, cmd == "buy", 5)
	case "craft":
		it, ok := FindItem(args)
		if !ok {
			return "Unknown item: " + args
		}
		d, _ := GetItemDetail(it.ID)
		if d.Craft == nil {
			return it.Name + " can't be crafted."
		}
		var ing []string
		for _, x := range d.Craft.Ingredients {
			ing = append(ing, fmt.Sprintf("%gx %s", x.Quantity, x.Name))
		}
		s := fmt.Sprintf("%s: %s", it.Name, strings.Join(ing, ", "))
		if d.Craft.Workbench != "None" && d.Craft.Workbench != "" {
			s += " | needs " + d.Craft.Workbench
		}
		return s
	case "recycle", "grecycle":
		it, ok := FindItem(args)
		if !ok {
			return "Unknown item: " + args
		}
		d, _ := GetItemDetail(it.ID)
		ys := d.Recycle
		where := "Safe-zone recycler"
		if cmd == "grecycle" {
			ys, where = d.GRecycle, "Monument recycler"
		}
		if len(ys) == 0 {
			return it.Name + " can't be recycled."
		}
		return fmt.Sprintf("%s (%s): %s", it.Name, where, fmtYield(ys))
	case "switch":
		f := strings.Fields(args)
		if len(f) < 2 {
			return "Usage: " + a.cfg.Settings.Prefix + "switch <entity id> <name>"
		}
		id, err := strconv.ParseUint(f[0], 10, 32)
		if err != nil {
			return "Entity id must be a number."
		}
		name := strings.ToLower(strings.Join(f[1:], ""))
		if builtinCommands[name] {
			return "'" + name + "' is a built-in command, pick another name."
		}
		sid := a.srv.ID
		a.mu.Unlock()
		a.AddDevice(sid, Device{EntityID: uint32(id), Name: strings.Join(f[1:], " "), Command: name})
		a.mu.Lock()
		return fmt.Sprintf("Switch %d is now '%s%s'.", id, a.cfg.Settings.Prefix, name)
	case "switches":
		var parts []string
		for _, d := range a.srv.Devices {
			if d.Type != rustplus.EntitySwitch {
				continue
			}
			state := "?"
			if ds := a.devState[d.EntityID]; ds != nil && ds.Payload != nil {
				state = onOff(ds.Payload.Value)
			}
			cmdName := d.Command
			if cmdName == "" {
				cmdName = fmt.Sprint(d.EntityID)
			}
			parts = append(parts, fmt.Sprintf("%s%s (%s): %s", a.cfg.Settings.Prefix, cmdName, d.Name, state))
		}
		if len(parts) == 0 {
			return "No switches paired. Pair one in-game (hold E > Pair) or use " + a.cfg.Settings.Prefix + "switch <id> <name>."
		}
		return strings.Join(parts, " | ")
	case "upkeep":
		var parts []string
		for _, d := range a.srv.Devices {
			if d.Type != rustplus.EntityStorageMonitor {
				continue
			}
			if ds := a.devState[d.EntityID]; ds != nil && ds.Payload != nil && ds.Payload.HasProtection {
				parts = append(parts, fmt.Sprintf("%s: %s", d.Name, fmtDur(time.Until(time.Unix(int64(ds.Payload.ProtectionExpiry), 0)))))
			}
		}
		if len(parts) == 0 {
			return "No tool cupboard storage monitors paired."
		}
		return "Upkeep: " + strings.Join(parts, " | ")
	case "promote", "leader":
		if a.team == nil {
			return "Team info not loaded."
		}
		if sender != a.team.LeaderSteamID {
			return "Only the team leader can do that."
		}
		target := a.matchMemberLocked(args)
		if target == nil {
			if args == "" {
				target = a.memberByIDLocked(sender)
			}
			if target == nil {
				return "No teammate called '" + args + "'."
			}
		}
		c := a.client
		if c == nil {
			return "Not connected."
		}
		a.mu.Unlock()
		err := c.PromoteToLeader(ctx, target.SteamID)
		a.mu.Lock()
		if err != nil {
			return "Couldn't promote: " + err.Error() + " (the bot's account must be the leader)."
		}
		return target.Name + " is now the team leader."
	case "tracked":
		return a.tracker.chatSummaryLocked()
	case "timer":
		f := strings.Fields(args)
		if len(f) < 2 {
			return "Usage: " + a.cfg.Settings.Prefix + "timer <15s|20m|3h> <message>"
		}
		d, err := parseDuration(f[0])
		if err != nil {
			return "Bad duration. Use e.g. 15s, 20m, 3h or 1h30m."
		}
		msg := strings.Join(f[1:], " ")
		a.addTimerLocked(d, msg)
		return fmt.Sprintf("Timer set: '%s' in %s.", msg, fmtDur(d))
	case "timers":
		if len(a.timers) == 0 {
			return "No active timers."
		}
		var parts []string
		for _, t := range a.timers {
			parts = append(parts, fmt.Sprintf("%s in %s", t.Message, fmtDur(time.Until(t.Fires))))
		}
		return strings.Join(parts, " | ")
	}
	// named switch toggles: !turrets, !turrets on, !turrets off
	for _, d := range a.srv.Devices {
		if d.Type == rustplus.EntitySwitch && d.Command != "" && d.Command == cmd {
			want := true
			ds := a.devState[d.EntityID]
			switch strings.ToLower(args) {
			case "on", "1":
				want = true
			case "off", "0":
				want = false
			default:
				if ds != nil && ds.Payload != nil {
					want = !ds.Payload.Value
				}
			}
			c := a.client
			if c == nil {
				return "Not connected."
			}
			a.mu.Unlock()
			err := c.SetEntityValue(ctx, d.EntityID, want)
			a.mu.Lock()
			if err != nil {
				return fmt.Sprintf("Couldn't switch %s: %v", d.Name, err)
			}
			return fmt.Sprintf("%s turned %s.", d.Name, onOff(want))
		}
	}
	return ""
}

// nextLine is the forecast sentence for chat replies. Caller holds a.mu.
func (a *App) nextLine(kind string) string {
	for _, f := range a.forecastsLocked() {
		if f.Kind == kind {
			switch f.State {
			case "upcoming":
				return "Next: " + f.Short + "."
			case "window":
				return "Next: due now, " + strings.TrimPrefix(f.Short, "could spawn any time · ") + "."
			case "active":
				return "Next after this: " + strings.TrimPrefix(f.Short, "on the map now · next ") + "."
			case "overdue":
				return "Next: " + f.Short + "."
			}
		}
	}
	return ""
}

func (a *App) alertLockedForce(title, text string) {
	e := LogEntry{Time: time.Now().Unix(), Kind: "rustplus", Title: title, Text: text}
	a.logs = append(a.logs, e)
	go a.publish("alert", map[string]any{"entry": e, "sound": a.cfg.Settings.Sound, "desktop": true})
}

func onOff(v bool) string {
	if v {
		return "ON"
	}
	return "OFF"
}

func listOr(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

func (a *App) matchMemberLocked(name string) *rustplus.Member {
	n := strings.ToLower(strings.TrimSpace(name))
	if a.team == nil || n == "" {
		return nil
	}
	var partial *rustplus.Member
	for i := range a.team.Members {
		m := &a.team.Members[i]
		mn := strings.ToLower(m.Name)
		if mn == n {
			return m
		}
		if strings.Contains(mn, n) && partial == nil {
			partial = m
		}
	}
	return partial
}

func (a *App) memberByIDLocked(id uint64) *rustplus.Member {
	if a.team == nil {
		return nil
	}
	for i := range a.team.Members {
		if a.team.Members[i].SteamID == id {
			return &a.team.Members[i]
		}
	}
	return nil
}

var durRe = regexp.MustCompile(`^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$`)

func parseDuration(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return time.Duration(n) * time.Minute, nil
	}
	m := durRe.FindStringSubmatch(s)
	if m == nil || s == "" {
		return 0, fmt.Errorf("bad duration")
	}
	var d time.Duration
	for i, unit := range []time.Duration{time.Hour, time.Minute, time.Second} {
		if m[i+1] != "" {
			n, _ := strconv.Atoi(m[i+1])
			d += time.Duration(n) * unit
		}
	}
	if d <= 0 || d > 48*time.Hour {
		return 0, fmt.Errorf("bad duration")
	}
	return d, nil
}

func (a *App) addTimerLocked(d time.Duration, msg string) *ChatTimer {
	a.timerSeq++
	ctx, cancel := context.WithCancel(context.Background())
	t := &ChatTimer{ID: a.timerSeq, Message: msg, Fires: time.Now().Add(d), cancel: cancel}
	a.timers = append(a.timers, t)
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
		a.mu.Lock()
		a.removeTimerLocked(t.ID)
		a.alertLockedForce("Timer", msg)
		a.mu.Unlock()
		a.SendChat("Timer: " + msg)
	}()
	return t
}

func (a *App) removeTimerLocked(id int) {
	var out []*ChatTimer
	for _, t := range a.timers {
		if t.ID == id {
			t.cancel()
			continue
		}
		out = append(out, t)
	}
	a.timers = out
}

// markersCutLocked: the server sends no shops to Rust+, as on every server since Rust's
// 6 Aug 2026 update (modded servers may still send them). Caller holds a.mu.
func (a *App) markersCutLocked() bool { return a.markerDiag.At != 0 && a.markerDiag.Vending == 0 }
