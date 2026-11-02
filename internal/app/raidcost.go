package app

import (
	"fmt"
	"strings"
)

// Raid costs for vanilla Rust, from full health, charges landing from outside. Figures from the
// Rust server files of 22 September 2026 (after the Breach and Clear update); a balance patch
// can change them.

type RaidTarget struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	HP      int      `json:"hp"`
	C4      int      `json:"c4"`
	Rockets int      `json:"rockets"`
	Satch   int      `json:"satchels"`
	Ammo    int      `json:"ammo"` // explosive 5.56 (upper figure)
	Bean    int      `json:"beancans"`
	Aliases []string `json:"-"`
}

var raidTargets = []RaidTarget{
	{"wood_wall", "Wood wall", 250, 1, 2, 3, 49, 18, []string{"wood", "wooden wall"}},
	{"stone_wall", "Stone wall", 500, 2, 4, 10, 185, 64, []string{"stone"}},
	{"metal_wall", "Sheet metal wall", 1000, 4, 8, 23, 400, 131, []string{"metal wall", "sheet metal", "metal"}},
	{"armored_wall", "Armored wall", 2000, 8, 15, 46, 799, 262, []string{"armored", "armoured wall", "hqm wall", "armour"}},
	{"wood_door", "Wooden door", 200, 1, 1, 2, 19, 7, []string{"wood door"}},
	{"metal_door", "Sheet metal door", 250, 1, 2, 4, 63, 21, []string{"metal door", "sheet door"}},
	{"garage_door", "Garage door", 600, 2, 3, 9, 150, 49, []string{"garage"}},
	{"armored_door", "Armored door", 1000, 3, 5, 15, 250, 82, []string{"armoured door", "hqm door"}},
	{"hatch", "Ladder hatch", 250, 1, 2, 4, 63, 21, []string{"ladder hatch"}},
	{"armored_hatch", "Armored ladder hatch", 1000, 3, 5, 15, 250, 82, []string{"armoured hatch", "hqm hatch"}},
	{"hew", "High external wooden wall", 500, 2, 3, 6, 98, 36, []string{"high wood", "high wooden", "hew", "external wood"}},
	{"hes", "High external stone wall", 500, 2, 4, 10, 185, 64, []string{"high stone", "hes", "external stone"}},
	{"bars", "Metal window bars", 500, 2, 4, 12, 200, 66, []string{"window bars", "bars"}},
	{"glass", "Strengthened glass window", 350, 2, 3, 9, 140, 46, []string{"glass", "window"}},
	{"tc", "Tool cupboard", 100, 1, 1, 1, 10, 4, []string{"cupboard", "tool cupboard"}},
}

// Sulfur per explosive.
var raidSulfur = map[string]int{"c4": 2200, "rockets": 1400, "satchels": 480, "ammo": 25, "beancans": 120}

func findRaidTarget(q string) *RaidTarget {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	for i := range raidTargets {
		t := &raidTargets[i]
		if q == t.Key || q == strings.ToLower(t.Name) {
			return t
		}
		for _, a := range t.Aliases {
			if q == a {
				return t
			}
		}
	}
	for i := range raidTargets {
		if strings.Contains(strings.ToLower(raidTargets[i].Name), q) {
			return &raidTargets[i]
		}
	}
	return nil
}

func commas(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// raidCostLine answers !raidcost <target> [count].
func raidCostLine(args, prefix string) string {
	f := strings.Fields(args)
	n := 1
	if len(f) > 1 {
		var k int
		if _, err := fmt.Sscan(f[len(f)-1], &k); err == nil && k > 0 && k <= 100 {
			n, f = k, f[:len(f)-1]
		}
	}
	t := findRaidTarget(strings.Join(f, " "))
	if t == nil {
		return "Usage: " + prefix + "raidcost <stone wall|metal wall|armored wall|metal door|garage door|armored door|hatch|tc…> [how many]"
	}
	what := t.Name
	if n > 1 {
		what = fmt.Sprintf("%d× %s", n, t.Name)
	}
	parts := []string{
		fmt.Sprintf("%d C4 (%s sulfur)", n*t.C4, commas(n*t.C4*raidSulfur["c4"])),
		fmt.Sprintf("%d rockets (%s)", n*t.Rockets, commas(n*t.Rockets*raidSulfur["rockets"])),
		fmt.Sprintf("%d satchels (%s)", n*t.Satch, commas(n*t.Satch*raidSulfur["satchels"])),
		fmt.Sprintf("%d explo ammo (%s)", n*t.Ammo, commas(n*t.Ammo*raidSulfur["ammo"])),
	}
	return what + ": " + strings.Join(parts, ", ") + "."
}
