package app

import (
	"fmt"
	"math"
	"strings"

	"rustwatch/internal/rustplus"
)

const gridSize = 146.25

func correctedMapSize(size float64) float64 {
	rem := math.Mod(size, gridSize)
	if rem < 120 {
		return size - rem
	}
	return size + gridSize - rem
}

func colLetters(n int) string { // 1-based
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}

// Grid returns the in-game grid reference (e.g. "G12") for world coordinates.
func Grid(x, y float32, mapSize uint32) string {
	ms := correctedMapSize(float64(mapSize))
	fx, fy := float64(x), float64(y)
	if ms <= 0 {
		return "?"
	}
	rows := int(math.Floor(ms / gridSize))
	switch {
	case fx < 0 && fy > ms:
		return "NW (off-grid)"
	case fx < 0 && fy < 0:
		return "SW (off-grid)"
	case fx > ms && fy > ms:
		return "NE (off-grid)"
	case fx > ms && fy < 0:
		return "SE (off-grid)"
	}
	col := int(math.Floor(fx/gridSize)) + 1
	row := rows - 1 - int(math.Floor(fy/gridSize))
	if col < 1 {
		return fmt.Sprintf("west of %d", clamp(row, 0, rows-1))
	}
	if fx > ms {
		return fmt.Sprintf("east of %d", clamp(row, 0, rows-1))
	}
	if fy < 0 {
		return "south of " + colLetters(col)
	}
	if fy > ms {
		return "north of " + colLetters(col)
	}
	return fmt.Sprintf("%s%d", colLetters(col), clamp(row, 0, rows-1))
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func dist(x1, y1, x2, y2 float32) float64 {
	dx, dy := float64(x1-x2), float64(y1-y2)
	return math.Sqrt(dx*dx + dy*dy)
}

func outsideGrid(x, y float32, mapSize uint32, offset float64) bool {
	ms := correctedMapSize(float64(mapSize))
	fx, fy := float64(x), float64(y)
	return fx < -offset || fx > ms+offset || fy < -offset || fy > ms+offset
}

var monumentNames = map[string]string{
	"outpost": "Outpost", "bandit_camp": "Bandit Camp", "oil_rig_small": "Small Oil Rig",
	"large_oil_rig": "Large Oil Rig", "launchsite": "Launch Site", "airfield_display_name": "Airfield",
	"harbor_display_name": "Harbor", "harbor_2_display_name": "Harbor", "train_yard_display_name": "Train Yard",
	"water_treatment_plant_display_name": "Water Treatment", "power_plant_display_name": "Power Plant",
	"military_tunnels_display_name": "Military Tunnels", "dome_monument_name": "The Dome",
	"satellite_dish_display_name": "Satellite Dish", "sewer_display_name": "Sewer Branch",
	"excavator": "Giant Excavator", "junkyard_display_name": "Junkyard", "fishing_village_display_name": "Fishing Village",
	"lighthouse_display_name": "Lighthouse", "mining_outpost_display_name": "Mining Outpost",
	"gas_station": "Oxum's Gas Station", "supermarket": "Abandoned Supermarket", "underwater_lab": "Underwater Lab",
	"stables_a": "Ranch", "stables_b": "Barn", "arctic_base_a": "Arctic Research Base", "missile_silo_monument": "Missile Silo",
	"mining_quarry_stone_display_name": "Stone Quarry", "mining_quarry_sulfur_display_name": "Sulfur Quarry",
	"mining_quarry_hqm_display_name": "HQM Quarry", "large_fishing_village_display_name": "Large Fishing Village",
	"swamp_c": "Abandoned Cabins", "ferryterminal": "Ferry Terminal", "AbandonedMilitaryBase": "Abandoned Military Base",
}

func monumentName(token string) string {
	if n, ok := monumentNames[token]; ok {
		return n
	}
	t := strings.TrimSuffix(strings.TrimSuffix(token, "_display_name"), "_monument_name")
	t = strings.ReplaceAll(t, "_", " ")
	if t == "" {
		return token
	}
	return strings.ToUpper(t[:1]) + t[1:]
}

// Location describes a position as "G12 (Outpost)" when near a monument.
func Location(x, y float32, mapSize uint32, mons []rustplus.Monument) string {
	g := Grid(x, y, mapSize)
	best := ""
	bd := 200.0
	for _, m := range mons {
		if m.Token == "DungeonBase" || strings.HasPrefix(m.Token, "train_tunnel") {
			continue
		}
		d := dist(x, y, m.X, m.Y)
		if d < bd {
			bd, best = d, monumentName(m.Token)
		}
	}
	if best != "" {
		return g + " (" + best + ")"
	}
	return g
}
