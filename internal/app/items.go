package app

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Item data from rustlabs (via the open-source rustplusplus project).
//
//go:embed data/itemdb.json
var itemDBRaw []byte

type ItemInfo struct {
	ID        int32  `json:"id"`
	Shortname string `json:"shortname"`
	Name      string `json:"name"`
}

type itemDB struct {
	byID    map[int32]ItemInfo
	list    []ItemInfo
	craft   map[int32]craftEntry
	recycle map[int32]recycleEntry
}

type craftEntry struct {
	Ingredients [][2]int64
	Workbench   int32
	Time        string
}

type yieldEntry struct {
	ID          int32
	Probability float64
	Quantity    float64
}

type recycleEntry struct {
	Monument []yieldEntry
	SafeZone []yieldEntry
}

var items = loadItems()

func loadItems() *itemDB {
	var raw struct {
		Items map[string]struct {
			S string `json:"s"`
			N string `json:"n"`
		} `json:"items"`
		Craft map[string]struct {
			I [][2]json.Number `json:"i"`
			W *string          `json:"w"`
			T string           `json:"t"`
		} `json:"craft"`
		Recycle map[string]struct {
			M [][3]json.Number `json:"m"`
			S [][3]json.Number `json:"s"`
		} `json:"recycle"`
	}
	dec := json.NewDecoder(strings.NewReader(string(itemDBRaw)))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		panic(err)
	}
	db := &itemDB{byID: map[int32]ItemInfo{}, craft: map[int32]craftEntry{}, recycle: map[int32]recycleEntry{}}
	atoi := func(s string) int32 { v, _ := strconv.ParseInt(s, 10, 64); return int32(v) }
	for k, v := range raw.Items {
		it := ItemInfo{ID: atoi(k), Shortname: v.S, Name: v.N}
		db.byID[it.ID] = it
		db.list = append(db.list, it)
	}
	sort.Slice(db.list, func(i, j int) bool { return db.list[i].Name < db.list[j].Name })
	for k, v := range raw.Craft {
		ce := craftEntry{Time: v.T}
		for _, ing := range v.I {
			id, _ := ing[0].Int64()
			q, _ := ing[1].Int64()
			ce.Ingredients = append(ce.Ingredients, [2]int64{id, q})
		}
		if v.W != nil {
			ce.Workbench = atoi(*v.W)
		}
		db.craft[atoi(k)] = ce
	}
	conv := func(in [][3]json.Number) []yieldEntry {
		var out []yieldEntry
		for _, y := range in {
			id, _ := y[0].Int64()
			p, _ := y[1].Float64()
			q, _ := y[2].Float64()
			out = append(out, yieldEntry{int32(id), p, q})
		}
		return out
	}
	for k, v := range raw.Recycle {
		db.recycle[atoi(k)] = recycleEntry{Monument: conv(v.M), SafeZone: conv(v.S)}
	}
	return db
}

func ItemName(id int32) string {
	if it, ok := items.byID[id]; ok {
		return it.Name
	}
	return fmt.Sprintf("Item %d", id)
}

func ItemShort(id int32) string {
	if it, ok := items.byID[id]; ok {
		return it.Shortname
	}
	return ""
}

// common player slang -> canonical item name
var itemAliases = map[string]string{
	"ak": "Assault Rifle", "ak47": "Assault Rifle", "ak 47": "Assault Rifle", "ar": "Assault Rifle",
	"lr": "LR-300 Assault Rifle", "lr300": "LR-300 Assault Rifle", "m2": "M249", "m249": "M249",
	"mp5": "MP5A4", "tommy": "Thompson", "thommy": "Thompson", "sar": "Semi-Automatic Rifle", "semi": "Semi-Automatic Rifle",
	"bolt": "Bolt Action Rifle", "l9": "L96 Rifle", "l96": "L96 Rifle", "dbl": "Double Barrel Shotgun", "db": "Double Barrel Shotgun",
	"pump": "Pump Shotgun", "spas": "Spas-12 Shotgun", "p2": "P250", "python": "Python Revolver", "revy": "Revolver",
	"custom": "Custom SMG", "smg": "Custom SMG", "m39": "M39 Rifle", "hmlmg": "HMLMG", "c4": "Timed Explosive Charge",
	"satchel": "Satchel Charge", "rocket": "Rocket", "rockets": "Rocket", "launcher": "Rocket Launcher", "rl": "Rocket Launcher",
	"tc": "Tool Cupboard", "cupboard": "Tool Cupboard", "hqm": "High Quality Metal", "frags": "Metal Fragments",
	"metal": "Metal Fragments", "lgf": "Low Grade Fuel", "fuel": "Low Grade Fuel", "gp": "Gun Powder", "sulfur": "Sulfur",
	"sulphur": "Sulfur", "explo": "Explosives", "explosive ammo": "Explosive 5.56 Rifle Ammo", "5.56": "5.56 Rifle Ammo",
	"556": "5.56 Rifle Ammo", "pistol ammo": "Pistol Bullet", "shells": "12 Gauge Buckshot", "garage": "Garage Door",
	"sheet": "Sheet Metal Door", "sheet door": "Sheet Metal Door", "armored door": "Armored Door", "hqm door": "Armored Door",
	"ladder hatch": "Ladder Hatch", "ladder": "Wooden Ladder", "furnace": "Furnace", "large furnace": "Large Furnace",
	"turret": "Auto Turret", "auto turret": "Auto Turret", "sam": "SAM Site", "mask": "Metal Facemask", "facemask": "Metal Facemask",
	"chest": "Metal Chest Plate", "chestplate": "Metal Chest Plate", "roadsign": "Road Sign Jacket", "road sign vest": "Road Sign Jacket", "roadsign vest": "Road Sign Jacket", "vest": "Road Sign Jacket", "level 1 workbench": "Workbench Level 1", "level 2 workbench": "Workbench Level 2", "level 3 workbench": "Workbench Level 3", "workbench": "Workbench Level 1", "wb": "Workbench Level 1", "kilt": "Road Sign Kilt",
	"gloves": "Tactical Gloves", "hazzy": "Hazmat Suit", "hazmat": "Hazmat Suit", "meds": "Medical Syringe", "syringe": "Medical Syringe",
	"medkit": "Large Medkit", "bandage": "Bandage", "scrap": "Scrap", "cloth": "Cloth", "leather": "Leather", "wood": "Wood",
	"stones": "Stones", "stone": "Stones", "tech trash": "Tech Trash", "techtrash": "Tech Trash", "pipe": "Metal Pipe",
	"pipes": "Metal Pipe", "springs": "Metal Spring", "spring": "Metal Spring", "rope": "Rope", "tarp": "Tarp", "gears": "Gears",
	"blade": "Metal Blade", "blades": "Metal Blade", "sewing": "Sewing Kit", "sewing kit": "Sewing Kit", "rifle body": "Rifle Body",
	"smg body": "SMG Body", "semi body": "Semi Automatic Body", "cctv": "CCTV Camera", "targeting": "Targeting Computer",
	"computer": "Targeting Computer", "wb1": "Workbench Level 1", "wb2": "Workbench Level 2", "wb3": "Workbench Level 3",
	"t1": "Workbench Level 1", "t2": "Workbench Level 2", "t3": "Workbench Level 3", "minicopter": "Minicopter",
	"mini": "Minicopter", "heli": "Minicopter", "scrapy": "Scrap Transport Helicopter", "boat": "Rowboat", "rhib": "RHIB",
	"jackhammer": "Jackhammer", "chainsaw": "Chainsaw", "salvaged axe": "Salvaged Axe", "pickaxe": "Pickaxe", "hatchet": "Hatchet",
	"mlrs": "MLRS Rocket", "nade": "F1 Grenade", "f1": "F1 Grenade", "beancan": "Beancan Grenade", "incen": "Incendiary Rocket",
	"hv": "High Velocity Rocket", "hv rocket": "High Velocity Rocket", "bag": "Sleeping Bag", "bed": "Bed", "coffin": "Coffin",
	"cards": "Green Keycard", "green card": "Green Keycard", "blue card": "Blue Keycard", "red card": "Red Keycard",
	"fuse": "Electric Fuse", "diesel": "Diesel Fuel", "barrel": "Diesel Fuel",
}

func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' {
			b.WriteRune(r)
			lastSpace = false
		} else if !lastSpace {
			b.WriteRune(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// SearchItems returns best matches for a fuzzy query (misspellings, shortnames, slang).
func SearchItems(q string, limit int) []ItemInfo {
	nq := normalize(q)
	if nq == "" {
		return nil
	}
	if id, err := strconv.ParseInt(nq, 10, 32); err == nil {
		if it, ok := items.byID[int32(id)]; ok {
			return []ItemInfo{it}
		}
	}
	if alias, ok := itemAliases[nq]; ok {
		nq = normalize(alias)
	}
	qTokens := strings.Fields(nq)
	type scored struct {
		it ItemInfo
		s  float64
	}
	var res []scored
	for _, it := range items.list {
		n := normalize(it.Name)
		sn := strings.ToLower(it.Shortname)
		var s float64
		switch {
		case n == nq || sn == nq:
			s = 1000
		case strings.HasPrefix(n, nq):
			s = 500 - float64(len(n)-len(nq))
		case strings.Contains(n, nq) || strings.Contains(sn, strings.ReplaceAll(nq, " ", ".")):
			s = 300 - float64(len(n)-len(nq))
		default:
			nTokens := strings.Fields(n)
			hit := 0
			for _, qt := range qTokens {
				for _, nt := range nTokens {
					if nt == qt || (len(qt) >= 3 && strings.HasPrefix(nt, qt)) {
						hit++
						break
					}
				}
			}
			if hit == len(qTokens) {
				s = 200 - float64(len(nTokens))
			} else {
				d := levenshtein(nq, n)
				if d <= max(2, len(nq)/4) {
					s = 100 - float64(d*10)
				} else if hit > 0 {
					s = float64(hit*10) - float64(len(nTokens))
				}
			}
		}
		if s > 0 {
			// prefer non-skin base items
			if strings.Contains(sn, "skin") || strings.HasPrefix(sn, "card") {
				s -= 50
			}
			res = append(res, scored{it, s})
		}
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].s > res[j].s })
	if limit <= 0 || limit > len(res) {
		limit = len(res)
	}
	out := make([]ItemInfo, 0, limit)
	for _, r := range res[:limit] {
		out = append(out, r.it)
	}
	return out
}

func FindItem(q string) (ItemInfo, bool) {
	r := SearchItems(q, 1)
	if len(r) == 0 {
		return ItemInfo{}, false
	}
	return r[0], true
}

type ItemDetail struct {
	ItemInfo
	Craft    *CraftView  `json:"craft,omitempty"`
	Recycle  []YieldView `json:"recycle"`
	GRecycle []YieldView `json:"grecycle"`
	Icon     string      `json:"icon"`
}

type CraftView struct {
	Ingredients []QtyView `json:"ingredients"`
	Workbench   string    `json:"workbench"`
	Time        string    `json:"time"`
}

type QtyView struct {
	Name     string  `json:"name"`
	ID       int32   `json:"id"`
	Quantity float64 `json:"quantity"`
	Chance   float64 `json:"chance,omitempty"`
}

type YieldView = QtyView

func GetItemDetail(id int32) (ItemDetail, bool) {
	it, ok := items.byID[id]
	if !ok {
		return ItemDetail{}, false
	}
	d := ItemDetail{ItemInfo: it, Icon: "https://rustlabs.com/img/items180/" + it.Shortname + ".png"}
	if c, ok := items.craft[id]; ok {
		cv := &CraftView{Time: c.Time, Workbench: "None"}
		if c.Workbench != 0 {
			cv.Workbench = ItemName(c.Workbench)
		}
		for _, ing := range c.Ingredients {
			cv.Ingredients = append(cv.Ingredients, QtyView{Name: ItemName(int32(ing[0])), ID: int32(ing[0]), Quantity: float64(ing[1])})
		}
		d.Craft = cv
	}
	if r, ok := items.recycle[id]; ok {
		for _, y := range r.SafeZone {
			d.Recycle = append(d.Recycle, QtyView{Name: ItemName(y.ID), ID: y.ID, Quantity: y.Quantity, Chance: y.Probability})
		}
		for _, y := range r.Monument {
			d.GRecycle = append(d.GRecycle, QtyView{Name: ItemName(y.ID), ID: y.ID, Quantity: y.Quantity, Chance: y.Probability})
		}
	}
	return d, true
}

func fmtYield(ys []YieldView) string {
	var parts []string
	for _, y := range ys {
		s := fmt.Sprintf("%gx %s", y.Quantity, y.Name)
		if y.Chance > 0 && y.Chance < 1 {
			s += fmt.Sprintf(" (%d%%)", int(y.Chance*100+0.5))
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}
