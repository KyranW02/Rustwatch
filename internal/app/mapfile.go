package app

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rustwatch/internal/rustmap"
	"rustwatch/internal/rustplus"
)

// Map file layers: Rust keeps the full world data of every server you've joined in its "maps"
// folder. RustWatch finds the one for the server it's showing (same size, monuments in the same
// places) and reads rock formations, caves, train tunnel entrances, biomes, beaches (where fresh
// spawns land) and monument no-build areas from it, plus an estimated cargo route. It only reads
// the file; it never touches the game.

type MapFiles struct {
	a  *App
	mu sync.Mutex

	key      string // server + size this result is for
	state    string // "", "searching", "ok", "missing", "error"
	note     string
	file     string
	fileTime time.Time
	world    *rustmap.World
	feats    []rustmap.Feature
	route    [][2]float32
	overlays map[string][]byte
	version  int
	busy     bool
	tried    time.Time
}

// retry looks again every couple of minutes while the map file is missing (you may join the
// server in Rust while RustWatch is running).
func (m *MapFiles) retry() {
	m.mu.Lock()
	due := m.state == "missing" && !m.busy && time.Since(m.tried) > 2*time.Minute
	m.mu.Unlock()
	if due {
		m.Look(true)
	}
}

func newMapFiles(a *App) *MapFiles { return &MapFiles{a: a, overlays: map[string][]byte{}} }

// mapDirs lists folders that may hold Rust's cached map files.
func mapDirs() []string {
	var out []string
	if d := os.Getenv("RUSTWATCH_MAPS"); d != "" {
		out = append(out, d)
	}
	for _, r := range rustRoots() {
		out = append(out, filepath.Join(r, "maps"))
	}
	return out
}

type MapFileView struct {
	State   string `json:"state"`
	Note    string `json:"note"`
	File    string `json:"file"`
	Version int    `json:"version"`
}

func (m *MapFiles) View() MapFileView {
	m.mu.Lock()
	defer m.mu.Unlock()
	return MapFileView{State: m.state, Note: m.note, File: m.file, Version: m.version}
}

// Look starts finding the map file for the current server (in the background).
func (m *MapFiles) Look(force bool) {
	a := m.a
	a.mu.Lock()
	if a.srv == nil || a.info == nil || a.mapData == nil {
		a.mu.Unlock()
		return
	}
	key := a.srv.ID + ":" + itoa(int(a.info.MapSize)) + ":" + itoa(int(a.info.WipeTime))
	size := a.info.MapSize
	mons := append([]rustplus.Monument(nil), a.mapData.Monuments...)
	remembered := a.srv.MapFile
	a.mu.Unlock()
	m.mu.Lock()
	if m.busy || (m.key == key && !force && m.state != "") {
		m.mu.Unlock()
		return
	}
	m.busy, m.key, m.state, m.note, m.tried = true, key, "searching", "Looking through Rust's map files…", time.Now()
	m.mu.Unlock()
	go func() {
		err := m.find(key, size, mons, remembered, force)
		m.mu.Lock()
		m.busy = false
		if err != nil && m.key == key {
			if m.state != "missing" {
				m.state = "error"
			}
			m.note = err.Error()
			m.version++
		}
		m.mu.Unlock()
		a.publish("update", nil)
	}()
}

func itoa(n int) string { return strconv.Itoa(n) }

// matchScore is the share of Rust+'s monuments that have a monument in the map file nearby.
func matchScore(mons []rustplus.Monument, spots [][2]float32) float64 {
	n, hit := 0, 0
	for _, m := range mons {
		if strings.HasPrefix(m.Token, "DungeonBase") || strings.Contains(m.Token, "train_tunnel") {
			continue
		}
		n++
		for _, s := range spots {
			if math.Hypot(float64(s[0]-m.X), float64(s[1]-m.Y)) < 90 {
				hit++
				break
			}
		}
	}
	if n == 0 {
		return 0
	}
	return float64(hit) / float64(n)
}

func (m *MapFiles) find(key string, size uint32, mons []rustplus.Monument, remembered string, force bool) error {
	type cand struct {
		path string
		mod  time.Time
	}
	var cands []cand
	dirs := mapDirs()
	for _, d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".map") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			cands = append(cands, cand{filepath.Join(d, e.Name()), info.ModTime()})
		}
	}
	if len(dirs) == 0 {
		m.setMissing("Couldn't find Rust's install folder on this PC.")
		return errors.New("couldn't find Rust's install folder on this PC")
	}
	// the one used last time first, then newest first: you most likely joined this server recently
	sort.Slice(cands, func(i, j int) bool {
		if (filepath.Base(cands[i].path) == remembered) != (filepath.Base(cands[j].path) == remembered) {
			return filepath.Base(cands[i].path) == remembered
		}
		return cands[i].mod.After(cands[j].mod)
	})
	var best *rustmap.World
	bestPath, bestScore := "", 0.0
	var bestMod time.Time
	for _, c := range cands {
		head, err := rustmap.Load(c.path, rustmap.Options{SizeOnly: true})
		if err != nil || head.Size != size {
			continue
		}
		w, err := rustmap.Load(c.path, rustmap.Options{Prefabs: true})
		if err != nil {
			continue
		}
		s := matchScore(mons, w.MonumentSpots())
		if s > bestScore {
			best, bestPath, bestScore, bestMod = w, c.path, s, c.mod
		}
		if s >= 0.9 {
			break
		}
	}
	if best == nil || bestScore < 0.6 {
		m.setMissing("No map file for this server yet. Join the server in Rust once (the game downloads its map then), and RustWatch picks it up.")
		return nil
	}
	full, err := rustmap.Load(bestPath, rustmap.Options{Prefabs: true, Layers: true})
	if err != nil {
		return err
	}
	feats := full.Features()
	route := full.CargoRoute()
	m.mu.Lock()
	if m.key == key {
		m.world, m.feats, m.route, m.overlays = full, feats, route, map[string][]byte{}
		m.file, m.fileTime, m.state = filepath.Base(bestPath), bestMod, "ok"
		m.note = ""
		m.version++
	}
	m.mu.Unlock()
	a := m.a
	a.mu.Lock()
	if a.srv != nil && a.srv.MapFile != filepath.Base(bestPath) {
		a.srv.MapFile = filepath.Base(bestPath)
		a.saveLocked()
	}
	a.mu.Unlock()
	return nil
}

func (m *MapFiles) setMissing(note string) {
	m.mu.Lock()
	m.state, m.note, m.world, m.feats, m.route = "missing", note, nil, nil, nil
	m.overlays = map[string][]byte{}
	m.version++
	m.mu.Unlock()
}

// Data is what the map page draws: points of interest and the cargo route.
type MapFileData struct {
	State     string            `json:"state"`
	File      string            `json:"file"`
	Size      uint32            `json:"size"`
	Features  []rustmap.Feature `json:"features"`
	Cargo     [][2]float32      `json:"cargo"`
	RockNames map[string]string `json:"rockNames"`
	Version   int               `json:"version"`
}

func (m *MapFiles) Data() MapFileData {
	m.a.mu.Lock()
	names := map[string]string{}
	for k, v := range m.a.cfg.Settings.RockNames {
		names[k] = v
	}
	m.a.mu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	d := MapFileData{State: m.state, File: m.file, Features: m.feats, Cargo: m.route, RockNames: names, Version: m.version}
	if d.Features == nil {
		d.Features = []rustmap.Feature{}
	}
	if m.world != nil {
		d.Size = m.world.Size
	}
	return d
}

// Overlay returns one terrain layer as a PNG (cached).
func (m *MapFiles) Overlay(layer string) ([]byte, error) {
	m.mu.Lock()
	w := m.world
	if b := m.overlays[layer]; b != nil {
		m.mu.Unlock()
		return b, nil
	}
	m.mu.Unlock()
	if w == nil {
		return nil, errors.New("no map file loaded")
	}
	b, ok := w.Overlay(layer)
	if !ok {
		return nil, errors.New("unknown layer")
	}
	m.mu.Lock()
	if m.world == w {
		m.overlays[layer] = b
	}
	m.mu.Unlock()
	return b, nil
}

// SetRockName names a rock model (e.g. "large_c" -> "God rock") for every map.
func (a *App) SetRockName(model, name string) error {
	model, name = strings.TrimSpace(model), strings.TrimSpace(name)
	if model == "" || len(model) > 40 || len(name) > 40 {
		return errors.New("bad rock name")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg.Settings.RockNames == nil {
		a.cfg.Settings.RockNames = map[string]string{}
	}
	if name == "" {
		delete(a.cfg.Settings.RockNames, model)
	} else {
		a.cfg.Settings.RockNames[model] = name
	}
	a.saveLocked()
	return nil
}
