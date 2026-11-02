package fakerust

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"path"
	"strings"

	"rustwatch/internal/rustmap"
)

type fakeMonument struct {
	token string
	x, y  float32
}

// UseMapFile makes the fake server look like the world in a real Rust .map file (size,
// monuments and a map picture), for demos and tests of the map-file layers.
func (s *Server) UseMapFile(file string) error {
	w, err := rustmap.Load(file, rustmap.Options{Prefabs: true, Layers: true})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MapSize = w.Size
	s.monuments = nil
	for _, p := range w.Prefabs {
		if p.Category != "Monument" {
			continue
		}
		x, y := w.ToMap(p.X, p.Z)
		tok := "monument"
		if n := p.Name(); n != "" {
			if strings.Contains(n, "/cave/") || strings.Contains(n, "tunnel-entrance") {
				continue // Rust+ doesn't list these as monuments
			}
			tok = strings.TrimSuffix(path.Base(n), ".prefab")
		}
		s.monuments = append(s.monuments, fakeMonument{tok, x, y})
	}
	// a Rust+-style picture: 1000 px with a 50 px ocean margin
	const W, margin = 1000, 50
	img := image.NewRGBA(image.Rect(0, 0, W, W))
	cols := []color.RGBA{{196, 170, 112, 255}, {92, 128, 66, 255}, {136, 132, 100, 255}, {222, 228, 232, 255}, {54, 112, 64, 255}}
	for py := 0; py < W; py++ {
		for px := 0; px < W; px++ {
			fx := (float64(px) - margin) / (W - 2*margin)
			fz := 1 - (float64(py)-margin)/(W-2*margin)
			c := color.RGBA{30, 62, 78, 255}
			if fx >= 0 && fx < 1 && fz >= 0 && fz < 1 {
				hx, hz := int(fx*float64(w.HeightRes)), int(fz*float64(w.HeightRes))
				h := w.Height[hz*w.HeightRes+hx]
				if h > 0 {
					c = cols[int(w.Biome[int(fz*float64(w.BiomeRes))*w.BiomeRes+int(fx*float64(w.BiomeRes))])%5]
					shade := 0.85 + 0.15*float64(min(h, 80))/80
					c.R, c.G, c.B = uint8(float64(c.R)*shade), uint8(float64(c.G)*shade), uint8(float64(c.B)*shade)
					if t := w.Topo[int(fz*float64(w.TopoRes))*w.TopoRes+int(fx*float64(w.TopoRes))]; t&rustmap.TopoRoad != 0 {
						c = color.RGBA{150, 130, 110, 255}
					} else if t&(rustmap.TopoRiver|rustmap.TopoLake) != 0 {
						c = color.RGBA{50, 90, 110, 255}
					}
				} else if h > -6 {
					c = color.RGBA{44, 84, 100, 255}
				}
			}
			img.SetRGBA(px, py, c)
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 85})
	s.jpg, s.mapW, s.margin = b.Bytes(), W, margin
	return nil
}
