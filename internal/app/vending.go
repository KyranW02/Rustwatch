package app

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"rustwatch/internal/rustplus"
)

type VendingMachine struct {
	Key        string               `json:"key"`
	ID         uint32               `json:"id"`
	X          float32              `json:"x"`
	Y          float32              `json:"y"`
	Name       string               `json:"name"`
	Grid       string               `json:"grid"`
	OutOfStock bool                 `json:"outOfStock"`
	Orders     []rustplus.SellOrder `json:"orders"`
	FirstSeen  int64                `json:"firstSeen"`
}

type VendingState struct {
	machines  map[string]*VendingMachine
	first     bool
	mapSize   uint32
	monuments []rustplus.Monument
}

func newVendingState() *VendingState {
	return &VendingState{machines: map[string]*VendingMachine{}, first: true}
}

func vmKey(x, y float32) string { return fmt.Sprintf("%.0f,%.0f", x, y) }

func orderKey(o rustplus.SellOrder) string {
	return fmt.Sprintf("%d/%v/%d/%d/%d", o.ItemID, o.ItemIsBlueprint, o.CurrencyID, o.CostPerItem, o.Quantity)
}

func describeOrder(o rustplus.SellOrder) string {
	item := ItemName(o.ItemID)
	if o.ItemIsBlueprint {
		item += " BP"
	}
	cur := ItemName(o.CurrencyID)
	if o.CurrencyIsBlueprint {
		cur += " BP"
	}
	return fmt.Sprintf("%dx %s for %d %s (%d in stock)", o.Quantity, item, o.CostPerItem, cur, o.AmountInStock)
}

// update diffs vending machines. Caller holds a.mu.
func (v *VendingState) update(markers []rustplus.Marker, a *App) {
	now := time.Now().Unix()
	next := map[string]*VendingMachine{}
	var watches []VendingWatch
	tracked := map[string]bool{}
	if a.srv != nil {
		watches = a.srv.Watches
		for _, k := range a.srv.TrackedVMs {
			tracked[k] = true
		}
	}
	for _, m := range markers {
		if m.Type != rustplus.MarkerVendingMachine {
			continue
		}
		k := vmKey(m.X, m.Y)
		vm := &VendingMachine{Key: k, ID: m.ID, X: m.X, Y: m.Y, Name: m.Name, OutOfStock: m.OutOfStock,
			Orders: m.SellOrders, Grid: Location(m.X, m.Y, v.mapSize, v.monuments), FirstSeen: now}
		if vm.Name == "" {
			vm.Name = "A Shop"
		}
		prev := v.machines[k]
		if prev != nil {
			vm.FirstSeen = prev.FirstSeen
		} else if !v.first {
			a.alertLocked("vending_new", "New vending machine", fmt.Sprintf("New vending machine '%s' at %s", vm.Name, vm.Grid))
		}
		prevOrders := map[string]bool{}
		if prev != nil {
			for _, o := range prev.Orders {
				prevOrders[orderKey(o)] = true
			}
		}
		if !v.first {
			cur := map[string]bool{}
			for _, o := range vm.Orders {
				ok := orderKey(o)
				cur[ok] = true
				isNew := !prevOrders[ok]
				if isNew && prev != nil && tracked[k] {
					a.alertLocked("vending_tracked", "Shop updated: "+vm.Name, fmt.Sprintf("%s (%s) new: %s", vm.Name, vm.Grid, describeOrder(o)))
				}
				if isNew && o.AmountInStock > 0 {
					for _, w := range watches {
						if w.ItemID == o.ItemID && (w.MaxPrice == 0 || o.CostPerItem <= w.MaxPrice) {
							a.alertLocked("vending_watch", ItemName(o.ItemID)+" for sale",
								fmt.Sprintf("%s for sale at %s (%s): %d %s", ItemName(o.ItemID), vm.Grid, vm.Name, o.CostPerItem, ItemName(o.CurrencyID)))
						}
					}
				}
			}
			if prev != nil && tracked[k] {
				for _, o := range prev.Orders {
					if !cur[orderKey(o)] {
						a.alertLocked("vending_tracked", "Shop updated: "+vm.Name, fmt.Sprintf("%s (%s) removed: %s", vm.Name, vm.Grid, describeOrder(o)))
					}
				}
			}
		}
		next[k] = vm
	}
	v.machines = next
	v.first = false
}

func (v *VendingState) list() []*VendingMachine {
	out := make([]*VendingMachine, 0, len(v.machines))
	for _, m := range v.machines {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Grid < out[j].Grid })
	return out
}

type VendingHit struct {
	Machine string             `json:"machine"`
	Key     string             `json:"key"`
	Grid    string             `json:"grid"`
	Order   rustplus.SellOrder `json:"order"`
	Text    string             `json:"text"`
}

// search finds orders selling (buy=true) or accepting (buy=false) the item.
func (v *VendingState) search(itemID int32, buy bool) []VendingHit {
	var hits []VendingHit
	for _, m := range v.machines {
		for _, o := range m.Orders {
			if o.AmountInStock <= 0 {
				continue
			}
			if (buy && o.ItemID == itemID) || (!buy && o.CurrencyID == itemID) {
				hits = append(hits, VendingHit{Machine: m.Name, Key: m.Key, Grid: m.Grid, Order: o, Text: describeOrder(o)})
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		pi := float64(hits[i].Order.CostPerItem) / float64(max(1, hits[i].Order.Quantity))
		pj := float64(hits[j].Order.CostPerItem) / float64(max(1, hits[j].Order.Quantity))
		if buy {
			return pi < pj
		}
		return pi > pj
	})
	return hits
}

func vendingChatSummary(hits []VendingHit, itemID int32, buy bool, limit int) string {
	name := ItemName(itemID)
	if len(hits) == 0 {
		if buy {
			return "Nobody is selling " + name + " right now."
		}
		return "Nobody is buying " + name + " right now."
	}
	var parts []string
	for i, h := range hits {
		if i >= limit {
			parts = append(parts, fmt.Sprintf("+%d more", len(hits)-limit))
			break
		}
		o := h.Order
		g := strings.SplitN(h.Grid, " ", 2)[0]
		if buy {
			parts = append(parts, fmt.Sprintf("%s: %dx for %d %s (%d left)", g, o.Quantity, o.CostPerItem, ItemName(o.CurrencyID), o.AmountInStock))
		} else {
			parts = append(parts, fmt.Sprintf("%s: gives %dx %s for %d", g, o.Quantity, ItemName(o.ItemID), o.CostPerItem))
		}
	}
	verb := "Selling"
	if !buy {
		verb = "Buying"
	}
	return fmt.Sprintf("%s %s: %s", verb, name, strings.Join(parts, " | "))
}
