package rustmap

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// RUSTMAP=path/to/file.map runs the decoder against a real Rust map file.
func TestRealMap(t *testing.T) {
	path := os.Getenv("RUSTMAP")
	if path == "" {
		t.Skip("set RUSTMAP to a .map file")
	}
	start := time.Now()
	w, err := Load(path, Options{Layers: true, Prefabs: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("size %d, %d prefabs, %d paths, topo %d biome %d height %d, %v", w.Size, len(w.Prefabs), len(w.Paths), w.TopoRes, w.BiomeRes, w.HeightRes, time.Since(start))
	named := 0
	for _, p := range w.Prefabs {
		if p.Name() != "" {
			named++
		}
	}
	t.Logf("named prefabs: %d", named)
	s, err := Load(path, Options{SizeOnly: true})
	if err != nil || s.Size != w.Size {
		t.Fatalf("size only: %v %v", s, err)
	}
}

func TestLZ4Block(t *testing.T) {
	// "abcabcabcabcabcX": 3 literals, then a 12-byte match at offset 3, then a literal "X"
	src := []byte{0x38, 'a', 'b', 'c', 3, 0, 0x10, 'X'}
	dst := make([]byte, 16)
	if err := lz4Block(src, dst); err != nil || string(dst) != "abcabcabcabcabcX" {
		t.Fatalf("%q %v", dst, err)
	}
	if err := lz4Block([]byte{0x38, 'a', 'b', 'c', 9, 0}, make([]byte, 15)); err == nil {
		t.Error("accepted an offset before the start")
	}
}

func TestRealMapOverlays(t *testing.T) {
	path, out := os.Getenv("RUSTMAP"), os.Getenv("RUSTMAP_OUT")
	if path == "" || out == "" {
		t.Skip("set RUSTMAP and RUSTMAP_OUT")
	}
	w, err := Load(path, Options{Layers: true, Prefabs: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{"biomes", "spawns", "nobuild"} {
		b, ok := w.Overlay(l)
		if !ok {
			t.Fatal(l)
		}
		os.WriteFile(out+"/"+l+".png", b, 0o644)
	}
	kinds := map[string]int{}
	for _, f := range w.Features() {
		kinds[f.Kind]++
	}
	t.Logf("features %v, cargo route %d points, monuments %d", kinds, len(w.CargoRoute()), len(w.MonumentSpots()))
	f, _ := os.Create(out + "/route.txt")
	for _, p := range w.CargoRoute() {
		fmt.Fprintf(f, "%f %f\n", p[0], p[1])
	}
	f.Close()
}
