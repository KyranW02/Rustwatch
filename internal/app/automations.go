package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"rustwatch/internal/rustplus"
)

// Automations flip smart switches when something happens: a smart alarm goes off, RustWatch
// hears a raid, or you start "raid mode" (button or chat command). All through Rust+.

type Automation struct {
	ID        string `json:"id"`
	Trigger   string `json:"trigger"`   // "alarm:<entityId>", "alarm:any", "raid", "manual" (raid mode)
	SwitchID  uint32 `json:"switchId"`  // the smart switch to set
	On        bool   `json:"on"`        // turn it on (true) or off
	RevertMin int    `json:"revertMin"` // set it back after this many minutes (0 = leave it)
}

type autoAction struct {
	switchID uint32
	name     string
	on       bool
	revert   time.Duration
}

func (a *App) triggerLabelLocked(tr string) string {
	switch {
	case tr == "raid":
		return "a raid is heard"
	case tr == "manual":
		return "raid mode is started"
	case tr == "alarm:any":
		return "any smart alarm goes off"
	case strings.HasPrefix(tr, "alarm:"):
		id, _ := strconv.ParseUint(strings.TrimPrefix(tr, "alarm:"), 10, 32)
		for _, d := range a.srv.Devices {
			if uint64(d.EntityID) == id {
				return "alarm '" + d.Name + "' goes off"
			}
		}
		return "alarm #" + strings.TrimPrefix(tr, "alarm:") + " goes off"
	}
	return tr
}

// SaveAutomation adds or replaces a rule.
func (a *App) SaveAutomation(r Automation) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.srv == nil {
		return errors.New("connect to a server first")
	}
	if !(r.Trigger == "raid" || r.Trigger == "manual" || r.Trigger == "alarm:any" || strings.HasPrefix(r.Trigger, "alarm:")) {
		return errors.New("pick what starts it")
	}
	var sw *Device
	for i := range a.srv.Devices {
		if a.srv.Devices[i].EntityID == r.SwitchID && a.srv.Devices[i].Type == rustplus.EntitySwitch {
			sw = &a.srv.Devices[i]
		}
	}
	if sw == nil {
		return errors.New("pick a paired smart switch")
	}
	if r.RevertMin < 0 || r.RevertMin > 24*60 {
		return errors.New("turn back after 0 to 1440 minutes")
	}
	if r.ID == "" {
		r.ID = strconv.FormatInt(time.Now().UnixNano(), 36)
		a.srv.Automations = append(a.srv.Automations, r)
	} else {
		found := false
		for i := range a.srv.Automations {
			if a.srv.Automations[i].ID == r.ID {
				a.srv.Automations[i], found = r, true
			}
		}
		if !found {
			a.srv.Automations = append(a.srv.Automations, r)
		}
	}
	a.saveLocked()
	return nil
}

func (a *App) DeleteAutomation(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.srv == nil {
		return
	}
	out := a.srv.Automations[:0]
	for _, r := range a.srv.Automations {
		if r.ID != id {
			out = append(out, r)
		}
	}
	a.srv.Automations = out
	a.saveLocked()
}

// fireLocked runs every rule for a trigger. Caller holds a.mu; switching happens afterwards.
// undo runs the rules backwards (raid mode off).
func (a *App) fireLocked(triggers []string, why string, undo bool) int {
	if a.srv == nil {
		return 0
	}
	want := map[string]bool{}
	for _, t := range triggers {
		want[t] = true
	}
	var acts []autoAction
	for _, r := range a.srv.Automations {
		if !want[r.Trigger] {
			continue
		}
		name := fmt.Sprint(r.SwitchID)
		for _, d := range a.srv.Devices {
			if d.EntityID == r.SwitchID {
				name = d.Name
			}
		}
		on := r.On != undo
		acts = append(acts, autoAction{r.SwitchID, name, on, time.Duration(r.RevertMin) * time.Minute})
	}
	if len(acts) == 0 {
		return 0
	}
	c := a.client
	go a.runActions(c, acts, why, undo)
	return len(acts)
}

// fire is fireLocked for callers that don't hold a.mu.
func (a *App) fire(triggers []string, why string) {
	a.mu.Lock()
	a.fireLocked(triggers, why, false)
	a.mu.Unlock()
}

func (a *App) runActions(c *rustplus.Client, acts []autoAction, why string, undo bool) {
	var done, failed []string
	for _, x := range acts {
		err := rustplus.ErrNotConnected
		if c != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err = c.SetEntityValue(ctx, x.switchID, x.on)
			cancel()
		}
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", x.name, err))
			continue
		}
		done = append(done, fmt.Sprintf("%s %s", x.name, onoff(x.on)))
		if x.revert > 0 && !undo {
			x := x
			time.AfterFunc(x.revert, func() {
				a.mu.Lock()
				cl := a.client
				a.mu.Unlock()
				if cl == nil {
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if cl.SetEntityValue(ctx, x.switchID, !x.on) == nil {
					a.mu.Lock()
					a.alertLocked("automation", "Automation: "+x.name+" back "+onoff(!x.on), fmt.Sprintf("%s turned back %s after %s", x.name, onoff(!x.on), fmtDur(x.revert)))
					a.mu.Unlock()
					a.publish("update", nil)
				}
			})
		}
	}
	a.mu.Lock()
	text := ""
	if len(done) > 0 {
		text = "Switched " + strings.Join(done, ", ")
	}
	if len(failed) > 0 {
		if text != "" {
			text += ". "
		}
		text += "Couldn't switch " + strings.Join(failed, ", ")
	}
	a.alertLocked("automation", "Automation: "+why, text+".")
	a.mu.Unlock()
	a.publish("update", nil)
}

// RaidMode runs the "raid mode" rules (or reverses them).
func (a *App) RaidMode(on bool) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.srv == nil {
		return 0, errors.New("connect to a server first")
	}
	why := "raid mode on"
	if !on {
		why = "raid mode off"
	}
	n := a.fireLocked([]string{"manual"}, why, !on)
	if n == 0 {
		return 0, errors.New("raid mode has no switches yet: add a rule that starts when raid mode is started")
	}
	return n, nil
}

type AutomationView struct {
	Automation
	Text string `json:"text"`
}

func (a *App) automationsViewLocked() []AutomationView {
	out := []AutomationView{}
	for _, r := range a.srv.Automations {
		name := fmt.Sprint(r.SwitchID)
		for _, d := range a.srv.Devices {
			if d.EntityID == r.SwitchID {
				name = d.Name
			}
		}
		t := fmt.Sprintf("When %s, turn %s %s", a.triggerLabelLocked(r.Trigger), name, onoff(r.On))
		if r.RevertMin > 0 {
			t += fmt.Sprintf(", then back %s after %s", onoff(!r.On), fmtDur(time.Duration(r.RevertMin)*time.Minute))
		}
		out = append(out, AutomationView{r, t})
	}
	return out
}

func onoff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
