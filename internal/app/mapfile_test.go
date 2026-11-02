package app

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rustwatch/internal/pb"
	"rustwatch/internal/rustmap"
)

// writeTestMap writes a small .map file (uncompressed chunk) for a 3500 m world with the fake
// server's monuments, two rocks, a cave and a tunnel entrance, a beach strip and a monument area.
func writeTestMap(t *testing.T, dir, name string, size int, monuments [][2]float32) string {
	t.Helper()
	w := &pb.Writer{}
	w.Uint(1, uint64(size))
	layer := func(n string, data []byte) {
		m := &pb.Writer{}
		m.String(1, n)
		m.BytesField(2, data)
		w.Message(2, m)
	}
	const res = 64
	topo := make([]byte, 4*res*res)
	height := make([]byte, 2*65*65)
	biome := make([]byte, 5*res*res)
	for z := 0; z < res; z++ {
		for x := 0; x < res; x++ {
			var f uint32 = 1
			if z == 3 {
				f |= rustmap.TopoBeach
			}
			if x > 30 && x < 34 && z > 30 && z < 34 {
				f |= rustmap.TopoMonument
			}
			if z < 3 {
				f = rustmap.TopoOcean
			}
			binary.LittleEndian.PutUint32(topo[4*(z*res+x):], f)
			biome[1*res*res+z*res+x] = 255 // temperate
		}
	}
	for z := 0; z < 65; z++ {
		for x := 0; x < 65; x++ {
			h := 20.0
			if z < 3 {
				h = -10
			}
			binary.LittleEndian.PutUint16(height[2*(z*65+x):], uint16((h+500)/1000*32767))
		}
	}
	layer("terrain", make([]byte, 16))
	layer("topology", topo)
	layer("height", height)
	layer("biome", biome)
	prefab := func(cat, path string, x, y float32) {
		p := &pb.Writer{}
		p.String(1, cat)
		p.Uint(2, uint64(rustmap.PrefabID(path)))
		v := &pb.Writer{}
		v.Float(1, x-float32(size)/2)
		v.Float(2, 10)
		v.Float(3, y-float32(size)/2)
		p.Message(3, v)
		p.Message(4, &pb.Writer{})
		s := &pb.Writer{}
		s.Float(1, 1)
		s.Float(2, 1)
		s.Float(3, 1)
		p.Message(5, s)
		w.Message(3, p)
	}
	for _, m := range monuments {
		prefab("Monument", "assets/bundled/prefabs/autospawn/monument/small/sphere_tank.prefab", m[0], m[1])
	}
	prefab("Decor", "assets/bundled/prefabs/autospawn/decor/v3_rock_formations_large/rock_formation_c.prefab", 2000, 2000)
	prefab("Decor", "assets/bundled/prefabs/autospawn/decor/v3_arid_rock_formations/formation_large_arid_a.prefab", 2100, 2050)
	prefab("Monument", "assets/bundled/prefabs/autospawn/monument/cave/cave_small_hard.prefab", 2500, 2500)
	prefab("Monument", "assets/bundled/prefabs/autospawn/tunnel-entrance/entrance_bunker_a.prefab", 1500, 1200)
	body := w.Bytes()
	var f []byte
	f = binary.LittleEndian.AppendUint32(f, 10)
	f = binary.LittleEndian.AppendUint64(f, 1791530010000)
	f = binary.AppendUvarint(f, 0) // flags: stored
	f = binary.AppendUvarint(f, uint64(len(body)))
	f = append(f, body...)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, f, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

var fakeMons = [][2]float32{{1750, 1750}, {3300, 1800}, {200, 1500}, {2300, 900}, {1000, 450}, {1300, 2500}, {900, 2600}}

func TestMapFileFound(t *testing.T) {
	dir := t.TempDir()
	// a map of another server (other monuments) and one of the wrong size: both ignored
	other := make([][2]float32, len(fakeMons))
	for i, m := range fakeMons {
		other[i] = [2]float32{m[0] + 400, m[1] - 300}
	}
	writeTestMap(t, dir, "elsewhere.map", 3500, other)
	writeTestMap(t, dir, "small.map", 2000, fakeMons)
	writeTestMap(t, dir, "ours.map", 3500, fakeMons)
	t.Setenv("RUSTWATCH_MAPS", dir)
	a, _ := connected(t, nil)
	waitFor(t, "map file found", func() bool { return a.maps.View().State == "ok" })
	d := a.maps.Data()
	if d.File != "ours.map" || d.Size != 3500 {
		t.Fatalf("picked %q size %d (%s)", d.File, d.Size, a.maps.View().Note)
	}
	kinds := map[string]int{}
	for _, f := range d.Features {
		kinds[f.Kind]++
		if f.Kind == "cave" && f.Label != "Small cave, hard" {
			t.Errorf("cave label %q", f.Label)
		}
		if f.Kind == "tunnel" && (math.Abs(float64(f.X-1500)) > 1 || math.Abs(float64(f.Y-1200)) > 1) {
			t.Errorf("tunnel at %v,%v", f.X, f.Y)
		}
	}
	if kinds["rock"] != 2 || kinds["cave"] != 1 || kinds["tunnel"] != 1 {
		t.Errorf("features %v", kinds)
	}
	if len(d.Cargo) < 100 {
		t.Errorf("cargo route %d points", len(d.Cargo))
	}
	for _, l := range []string{"biomes", "spawns", "nobuild"} {
		b, err := a.maps.Overlay(l)
		if err != nil || len(b) < 100 {
			t.Errorf("%s: %d %v", l, len(b), err)
		}
	}
	if _, err := a.maps.Overlay("nope"); err == nil {
		t.Error("unknown layer served")
	}
	a.mu.Lock()
	remembered := a.srv.MapFile
	a.mu.Unlock()
	if remembered != "ours.map" {
		t.Errorf("remembered %q", remembered)
	}
	if err := a.SetRockName("large_c", "God rock"); err != nil || a.maps.Data().RockNames["large_c"] != "God rock" {
		t.Errorf("rock name %v %v", err, a.maps.Data().RockNames)
	}
}

func TestMapFileMissing(t *testing.T) {
	t.Setenv("RUSTWATCH_MAPS", t.TempDir())
	a, _ := connected(t, nil)
	waitFor(t, "missing", func() bool { return a.maps.View().State == "missing" })
	if !strings.Contains(a.maps.View().Note, "Join the server in Rust once") {
		t.Error(a.maps.View().Note)
	}
}
