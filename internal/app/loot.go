package app

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"rustwatch/internal/rustplus"
)

// Loot log: every change in a storage monitor's box, and an alert when a box is suddenly
// emptied (a raid, or a teammate cleaning out the loot room).

type LootChange struct {
	ID    int32  `json:"id"`
	Name  string `json:"name"`
	Short string `json:"short"`
	Delta int32  `json:"delta"`
	BP    bool   `json:"bp,omitempty"`
}

type LootEntry struct {
	Time     int64        `json:"time"`
	EntityID uint32       `json:"entityId"`
	Box      string       `json:"box"`
	Changes  []LootChange `json:"changes"`
	Emptied  bool         `json:"emptied,omitempty"`
}

type lootKey struct {
	id int32
	bp bool
}

func countItems(p *rustplus.Payload) map[lootKey]int32 {
	m := map[lootKey]int32{}
	for _, it := range p.Items {
		m[lootKey{it.ItemID, it.IsBlueprint}] += it.Quantity
	}
	return m
}

// recordLootLocked compares two full storage-monitor readings. Caller holds a.mu.
func (a *App) recordLootLocked(d Device, prev, cur *rustplus.Payload) {
	if a.srv == nil || d.Type != rustplus.EntityStorageMonitor || prev == nil || cur == nil || prev.Capacity == 0 || cur.Capacity == 0 {
		return
	}
	before, after := countItems(prev), countItems(cur)
	var ch []LootChange
	for k, q := range after {
		if dq := q - before[k]; dq != 0 {
			ch = append(ch, LootChange{k.id, ItemName(k.id), ItemShort(k.id), dq, k.bp})
		}
	}
	for k, q := range before {
		if _, ok := after[k]; !ok {
			ch = append(ch, LootChange{k.id, ItemName(k.id), ItemShort(k.id), -q, k.bp})
		}
	}
	if len(ch) == 0 {
		return
	}
	sort.Slice(ch, func(i, j int) bool { return abs32(ch[i].Delta) > abs32(ch[j].Delta) })
	// a box that held several stacks and lost most of them in one go
	emptied := len(prev.Items) >= 3 && len(cur.Items) <= len(prev.Items)/4
	e := LootEntry{Time: time.Now().Unix(), EntityID: d.EntityID, Box: d.Name, Changes: ch, Emptied: emptied}
	a.srv.LootLog = append(a.srv.LootLog, e)
	if len(a.srv.LootLog) > 400 {
		a.srv.LootLog = a.srv.LootLog[len(a.srv.LootLog)-400:]
	}
	a.saveLocked()
	if emptied {
		var lost []string
		for _, c := range ch {
			if c.Delta < 0 && len(lost) < 4 {
				lost = append(lost, fmt.Sprintf("%s %s", fmtQty(-c.Delta), c.Name))
			}
		}
		a.alertLocked("box_emptied", d.Name+" was emptied", fmt.Sprintf("%s went from %d stacks to %d: %s taken.", d.Name, len(prev.Items), len(cur.Items), strings.Join(lost, ", ")))
	}
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func fmtQty(q int32) string {
	if q >= 10000 {
		return fmt.Sprintf("%dk", q/1000)
	}
	if q >= 1000 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(q)/1000), ".0") + "k"
	}
	return fmt.Sprint(q)
}

// setPayloadLocked stores a new reading for a device, logging loot changes. Caller holds a.mu.
func (a *App) setPayloadLocked(dev *Device, ds *deviceState, p rustplus.Payload) {
	if dev != nil && dev.Type == rustplus.EntityStorageMonitor {
		a.recordLootLocked(*dev, ds.Payload, &p)
	}
	ds.Payload, ds.Error, ds.Updated = &p, "", time.Now()
}

// lootSummaryLocked answers !loot: the latest changes.
func (a *App) lootSummaryLocked() string {
	log := a.srv.LootLog
	if len(log) == 0 {
		return "No box changes seen yet. Pair a storage monitor on a box or TC."
	}
	var parts []string
	for i := len(log) - 1; i >= 0 && len(parts) < 3; i-- {
		e := log[i]
		var c []string
		for _, x := range e.Changes {
			if len(c) == 3 {
				break
			}
			sign := "+"
			if x.Delta < 0 {
				sign = "-"
			}
			c = append(c, fmt.Sprintf("%s%s %s", sign, fmtQty(abs32(x.Delta)), x.Name))
		}
		parts = append(parts, fmt.Sprintf("%s %s ago: %s", e.Box, fmtDur(time.Since(time.Unix(e.Time, 0))), strings.Join(c, ", ")))
	}
	return strings.Join(parts, " | ")
}
