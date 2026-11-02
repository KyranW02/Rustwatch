package rustmap

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
)

// Feature is a point of interest from the map file, in Rust+ map coordinates (0..size, y north).
type Feature struct {
	Kind  string  `json:"kind"`  // "rock", "cave", "tunnel", "iceberg"
	Model string  `json:"model"` // rock model key, e.g. "large_c" / "arid_a"
	Label string  `json:"label"`
	X     float32 `json:"x"`
	Y     float32 `json:"y"`
	Rot   float32 `json:"rot"`
	Scale float32 `json:"scale"`
}

// ToMap converts a world position (origin at the centre) to Rust+ map coordinates.
func (w *World) ToMap(x, z float32) (float32, float32) {
	h := float32(w.Size) / 2
	return x + h, z + h
}

func letter(name string) string {
	base := strings.TrimSuffix(name[strings.LastIndex(name, "/")+1:], ".prefab")
	return base[strings.LastIndex(base, "_")+1:]
}

// Features lists rock formations, caves, train tunnel entrances and icebergs.
func (w *World) Features() []Feature {
	var out []Feature
	for _, p := range w.Prefabs {
		n := p.Name()
		if n == "" {
			continue
		}
		x, y := w.ToMap(p.X, p.Z)
		f := Feature{X: x, Y: y, Rot: p.RotY, Scale: p.Scale}
		switch {
		case strings.Contains(n, "v3_rock_formations_large/"):
			f.Kind, f.Model = "rock", "large_"+letter(n)
			f.Label = "Large rock formation, model " + strings.ToUpper(letter(n))
		case strings.Contains(n, "formation_large_arid"):
			f.Kind, f.Model = "rock", "arid_"+letter(n)
			f.Label = "Large desert rock formation, model " + strings.ToUpper(letter(n))
		case strings.Contains(n, "/monument/cave/"):
			base := strings.TrimSuffix(n[strings.LastIndex(n, "/")+1:], ".prefab")
			parts := strings.Split(strings.TrimPrefix(base, "cave_"), "_")
			f.Kind = "cave"
			size, diff := parts[0], parts[len(parts)-1]
			f.Label = strings.ToUpper(size[:1]) + size[1:] + " cave"
			if strings.Contains(base, "sewers") {
				f.Label += " (sewers)"
			}
			if diff == "easy" || diff == "medium" || diff == "hard" {
				f.Label += ", " + diff
			}
		case strings.Contains(n, "/tunnel-entrance/"):
			f.Kind, f.Label = "tunnel", "Train tunnel entrance"
		case strings.Contains(n, "/decor/iceberg/"):
			f.Kind, f.Label = "iceberg", "Iceberg"
		default:
			continue
		}
		out = append(out, f)
	}
	return out
}

// MonumentSpots are the positions of monument prefabs, for matching against Rust+'s monuments.
func (w *World) MonumentSpots() [][2]float32 {
	var out [][2]float32
	for _, p := range w.Prefabs {
		if p.Category == "Monument" {
			x, y := w.ToMap(p.X, p.Z)
			out = append(out, [2]float32{x, y})
		}
	}
	return out
}

// ---- overlays ----

const overlayRes = 1024

func (w *World) topoAt(fx, fz float64) int32 {
	if w.TopoRes == 0 {
		return 0
	}
	x, z := int(fx*float64(w.TopoRes)), int(fz*float64(w.TopoRes))
	if x < 0 || z < 0 || x >= w.TopoRes || z >= w.TopoRes {
		return 0
	}
	return w.Topo[z*w.TopoRes+x]
}

func (w *World) heightAt(fx, fz float64) float32 {
	if w.HeightRes == 0 {
		return 1
	}
	x, z := int(fx*float64(w.HeightRes)), int(fz*float64(w.HeightRes))
	if x < 0 || z < 0 || x >= w.HeightRes || z >= w.HeightRes {
		return -10
	}
	return w.Height[z*w.HeightRes+x]
}

func sample(res int, data []byte, fx, fz float64) byte {
	if res == 0 {
		return 0
	}
	x, z := int(fx*float64(res)), int(fz*float64(res))
	if x < 0 || z < 0 || x >= res || z >= res {
		return 0
	}
	return data[z*res+x]
}

var biomeColours = []color.NRGBA{{232, 186, 96, 0}, {108, 170, 72, 0}, {150, 146, 110, 0}, {236, 244, 252, 0}, {30, 140, 80, 0}}

// Overlay draws one layer as a transparent image covering the whole world (top = north).
// Layers: "biomes", "spawns" (beaches), "nobuild" (monument areas).
func (w *World) Overlay(layer string) ([]byte, bool) {
	img := image.NewNRGBA(image.Rect(0, 0, overlayRes, overlayRes))
	for py := 0; py < overlayRes; py++ {
		fz := 1 - (float64(py)+0.5)/overlayRes
		for px := 0; px < overlayRes; px++ {
			fx := (float64(px) + 0.5) / overlayRes
			t := w.topoAt(fx, fz)
			land := w.heightAt(fx, fz) > 0.3 && t&(TopoOcean|TopoOffshore) == 0
			var c color.NRGBA
			switch layer {
			case "biomes":
				if land {
					c = biomeColours[int(sample(w.BiomeRes, w.Biome, fx, fz))%5]
					c.A = 120
				}
			case "spawns":
				if t&TopoBeach != 0 {
					c = color.NRGBA{255, 214, 102, 220}
				}
			case "nobuild":
				if t&TopoMonument != 0 {
					c = color.NRGBA{229, 72, 59, 120}
					if (px+py)%8 < 2 {
						c.A = 210 // hatching, so the map shows through
					}
				}
			default:
				return nil, false
			}
			img.SetNRGBA(px, py, c)
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes(), true
}

// CargoRoute estimates the cargo ship's loop: a ring a fixed distance off the coast, found by
// casting rays from the island's middle. Rust works out the real path from the coastline in a
// similar way when the server starts, so this is a close guide, not the exact path.
func (w *World) CargoRoute() [][2]float32 {
	if w.HeightRes == 0 {
		return nil
	}
	size := float64(w.Size)
	// land centroid
	var cx, cz, n float64
	step := w.HeightRes / 128
	if step < 1 {
		step = 1
	}
	for z := 0; z < w.HeightRes; z += step {
		for x := 0; x < w.HeightRes; x += step {
			if w.Height[z*w.HeightRes+x] > 0 {
				cx += float64(x)
				cz += float64(z)
				n++
			}
		}
	}
	if n == 0 {
		return nil
	}
	cx, cz = cx/n/float64(w.HeightRes)*size, cz/n/float64(w.HeightRes)*size
	const rays = 180
	radius := make([]float64, rays)
	for i := 0; i < rays; i++ {
		a := float64(i) / rays * 2 * math.Pi
		dx, dz := math.Cos(a), math.Sin(a)
		last := 0.0
		for r := 0.0; ; r += 10 {
			x, z := cx+dx*r, cz+dz*r
			if x < 0 || z < 0 || x >= size || z >= size {
				break
			}
			if w.heightAt(x/size, z/size) > 0 {
				last = r
			}
		}
		radius[i] = last + 260
	}
	// smooth, then keep inside the map
	sm := make([]float64, rays)
	for i := range radius {
		var s float64
		for k := -6; k <= 6; k++ {
			s += radius[(i+k+rays)%rays]
		}
		sm[i] = s / 13
	}
	var out [][2]float32
	for i := 0; i <= rays; i++ {
		a := float64(i%rays) / rays * 2 * math.Pi
		r := sm[i%rays]
		x, z := cx+math.Cos(a)*r, cz+math.Sin(a)*r
		x = math.Max(120, math.Min(size-120, x))
		z = math.Max(120, math.Min(size-120, z))
		out = append(out, [2]float32{float32(x), float32(z)})
	}
	return out
}
