// Package rustmap reads the .map files the Rust client keeps in its "maps" folder (the world
// data for every server you've joined): placed prefabs (monuments, rock formations, caves,
// tunnel entrances), roads/rails/rivers, and the terrain layers (height, biome, topology,
// splat). Format: uint32 version, int64 timestamp (version 10+), then a chunked LZ4 stream of
// a protobuf WorldData message.
package rustmap

import (
	"bufio"
	"crypto/md5"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

//go:embed prefabs.txt
var prefabList string

// prefab names by ID (the ID is the first 4 bytes of the MD5 of the lower-case asset path)
var prefabNames = map[uint32]string{}

func init() {
	for _, n := range strings.Split(prefabList, "\n") {
		if n = strings.TrimSpace(n); n != "" {
			prefabNames[PrefabID(n)] = n
		}
	}
}

// PrefabID is Rust's ID for an asset path.
func PrefabID(path string) uint32 {
	h := md5.Sum([]byte(strings.ToLower(path)))
	return binary.LittleEndian.Uint32(h[:4])
}

type Prefab struct {
	ID       uint32
	Category string
	X, Y, Z  float32 // world position, origin at the map centre, Y up
	RotY     float32
	Scale    float32
}

// Name is the asset path, if it's one RustWatch knows.
func (p Prefab) Name() string { return prefabNames[p.ID] }

type Path struct {
	Name  string
	Nodes [][2]float32 // world x, z
}

// Topology flags (TerrainTopology).
const (
	TopoField    = 1
	TopoCliff    = 2
	TopoBeach    = 16
	TopoForest   = 32
	TopoOcean    = 128
	TopoMonument = 1024
	TopoRoad     = 2048
	TopoRiver    = 16384
	TopoLake     = 65536
	TopoOffshore = 262144
	TopoRail     = 524288
	TopoMountain = 8388608
)

// Biomes, in the order of the biome map's channels.
var BiomeNames = []string{"Arid", "Temperate", "Tundra", "Arctic", "Jungle"}

type World struct {
	Version uint32
	Size    uint32
	Prefabs []Prefab
	Paths   []Path

	// terrain layers, row 0 = south edge, downsampled
	TopoRes   int
	Topo      []int32 // OR of the flags in each cell
	BiomeRes  int
	Biome     []byte // dominant biome per cell
	HeightRes int
	Height    []float32 // metres above sea level
}

// ---- chunked LZ4 stream ----

type lz4Stream struct {
	r   *bufio.Reader
	buf []byte
	pos int
	src []byte
}

func (s *lz4Stream) Read(p []byte) (int, error) {
	for s.pos >= len(s.buf) {
		flags, err := binary.ReadUvarint(s.r)
		if err != nil {
			return 0, err
		}
		orig, err := binary.ReadUvarint(s.r)
		if err != nil {
			return 0, io.ErrUnexpectedEOF
		}
		if orig > 64<<20 {
			return 0, errors.New("map chunk too large")
		}
		if cap(s.buf) < int(orig) {
			s.buf = make([]byte, orig)
		}
		s.buf, s.pos = s.buf[:orig], 0
		if flags&1 != 0 {
			clen, err := binary.ReadUvarint(s.r)
			if err != nil || clen > 64<<20 {
				return 0, io.ErrUnexpectedEOF
			}
			if cap(s.src) < int(clen) {
				s.src = make([]byte, clen)
			}
			s.src = s.src[:clen]
			if _, err := io.ReadFull(s.r, s.src); err != nil {
				return 0, io.ErrUnexpectedEOF
			}
			if err := lz4Block(s.src, s.buf); err != nil {
				return 0, err
			}
		} else if _, err := io.ReadFull(s.r, s.buf); err != nil {
			return 0, io.ErrUnexpectedEOF
		}
	}
	n := copy(p, s.buf[s.pos:])
	s.pos += n
	return n, nil
}

// ---- protobuf helpers ----

type pbReader struct{ *bufio.Reader }

func (r pbReader) tag() (int, int, error) {
	v, err := binary.ReadUvarint(r)
	return int(v >> 3), int(v & 7), err
}

func (r pbReader) skip(wire int) error {
	switch wire {
	case 0:
		_, err := binary.ReadUvarint(r)
		return err
	case 1:
		_, err := r.Discard(8)
		return err
	case 2:
		n, err := binary.ReadUvarint(r)
		if err != nil {
			return err
		}
		_, err = r.Discard(int(n))
		return err
	case 5:
		_, err := r.Discard(4)
		return err
	}
	return fmt.Errorf("bad wire type %d", wire)
}

// fields walks a message held in memory.
func fields(b []byte, fn func(num, wire int, v uint64, data []byte)) {
	for p := 0; p < len(b); {
		t, n := binary.Uvarint(b[p:])
		if n <= 0 {
			return
		}
		p += n
		num, wire := int(t>>3), int(t&7)
		switch wire {
		case 0:
			v, n := binary.Uvarint(b[p:])
			if n <= 0 {
				return
			}
			p += n
			fn(num, wire, v, nil)
		case 1:
			if p+8 > len(b) {
				return
			}
			fn(num, wire, binary.LittleEndian.Uint64(b[p:]), nil)
			p += 8
		case 2:
			l, n := binary.Uvarint(b[p:])
			if n <= 0 || p+n+int(l) > len(b) {
				return
			}
			p += n
			fn(num, wire, 0, b[p:p+int(l)])
			p += int(l)
		case 5:
			if p+4 > len(b) {
				return
			}
			fn(num, wire, uint64(binary.LittleEndian.Uint32(b[p:])), nil)
			p += 4
		default:
			return
		}
	}
}

func vec3(b []byte) (v [3]float32) {
	fields(b, func(num, wire int, x uint64, _ []byte) {
		if wire == 5 && num >= 1 && num <= 3 {
			v[num-1] = math.Float32frombits(uint32(x))
		}
	})
	return
}

// Options picks what Load keeps (the terrain layers are big).
type Options struct {
	SizeOnly bool // stop after the world size
	Layers   bool // keep terrain layers (downsampled)
	Prefabs  bool
}

// Load reads a .map file.
func Load(path string, opt Options) (*World, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Read(f, opt)
}

func Read(f io.Reader, opt Options) (*World, error) {
	br := bufio.NewReaderSize(f, 1<<20)
	w := &World{}
	if err := binary.Read(br, binary.LittleEndian, &w.Version); err != nil {
		return nil, err
	}
	if w.Version < 9 || w.Version > 20 {
		return nil, fmt.Errorf("unknown map file version %d", w.Version)
	}
	if w.Version >= 10 {
		var ts int64
		if err := binary.Read(br, binary.LittleEndian, &ts); err != nil {
			return nil, err
		}
	}
	r := pbReader{bufio.NewReaderSize(&lz4Stream{r: br}, 1<<20)}
	for {
		num, wire, err := r.tag()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch {
		case num == 1 && wire == 0:
			v, err := binary.ReadUvarint(r)
			if err != nil {
				return nil, err
			}
			w.Size = uint32(v)
			if opt.SizeOnly {
				return w, nil
			}
		case num == 2 && wire == 2:
			if err := w.readMap(r, opt.Layers); err != nil {
				return nil, err
			}
		case (num == 3 || num == 4) && wire == 2 && opt.Prefabs:
			n, err := binary.ReadUvarint(r)
			if err != nil || n > 1<<20 {
				return nil, errors.New("bad prefab entry")
			}
			b := make([]byte, n)
			if _, err := io.ReadFull(r, b); err != nil {
				return nil, err
			}
			if num == 3 {
				w.Prefabs = append(w.Prefabs, decodePrefab(b))
			} else {
				w.Paths = append(w.Paths, decodePath(b))
			}
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	if w.Size == 0 {
		return nil, errors.New("no world size in the map file")
	}
	return w, nil
}

func decodePrefab(b []byte) Prefab {
	p := Prefab{Scale: 1}
	fields(b, func(num, wire int, v uint64, d []byte) {
		switch num {
		case 1:
			p.Category = string(d)
		case 2:
			p.ID = uint32(v)
		case 3:
			x := vec3(d)
			p.X, p.Y, p.Z = x[0], x[1], x[2]
		case 4:
			p.RotY = vec3(d)[1]
		case 5:
			p.Scale = vec3(d)[0]
		}
	})
	return p
}

func decodePath(b []byte) Path {
	var p Path
	fields(b, func(num, wire int, v uint64, d []byte) {
		switch num {
		case 1:
			p.Name = string(d)
		case 15:
			x := vec3(d)
			p.Nodes = append(p.Nodes, [2]float32{x[0], x[2]})
		}
	})
	return p
}

// readMap reads one MapData {name, data}, keeping the layers RustWatch uses.
func (w *World) readMap(r pbReader, keep bool) error {
	total, err := binary.ReadUvarint(r)
	if err != nil {
		return err
	}
	name := ""
	var read uint64
	for read < total {
		num, wire, err := r.tag()
		if err != nil {
			return err
		}
		read++ // tags here are one byte
		switch {
		case num == 1 && wire == 2:
			n, err := binary.ReadUvarint(r)
			if err != nil || n > 64 {
				return errors.New("bad layer name")
			}
			b := make([]byte, n)
			if _, err := io.ReadFull(r, b); err != nil {
				return err
			}
			name = string(b)
			read += uint64(uvarintLen(n)) + n
		case num == 2 && wire == 2:
			n, err := binary.ReadUvarint(r)
			if err != nil {
				return err
			}
			read += uint64(uvarintLen(n)) + n
			want := keep && (name == "topology" || name == "biome" || name == "height")
			if !want {
				if _, err := r.Discard(int(n)); err != nil {
					return err
				}
				continue
			}
			if n > 128<<20 {
				return errors.New("layer too large")
			}
			b := make([]byte, n)
			if _, err := io.ReadFull(r, b); err != nil {
				return err
			}
			w.layer(name, b)
		default:
			return fmt.Errorf("unexpected field %d in a map layer", num)
		}
	}
	return nil
}

func uvarintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

func isqrt(n int) int { return int(math.Sqrt(float64(n)) + 0.5) }

// layer downsamples a terrain layer.
func (w *World) layer(name string, b []byte) {
	switch name {
	case "topology": // int32 flags, res×res
		res := isqrt(len(b) / 4)
		out := 1024
		if res < out {
			out = res
		}
		k := res / out
		t := make([]int32, out*out)
		for z := 0; z < res; z++ {
			for x := 0; x < res; x++ {
				v := int32(binary.LittleEndian.Uint32(b[4*(z*res+x):]))
				t[(z/k)*out+x/k] |= v
			}
		}
		w.TopoRes, w.Topo = out, t
	case "biome": // 5 channels, channel-major, res×res each
		res := isqrt(len(b) / 5)
		out := 512
		if res < out {
			out = res
		}
		k := res / out
		bm := make([]byte, out*out)
		plane := res * res
		for z := 0; z < out; z++ {
			for x := 0; x < out; x++ {
				i := (z*k)*res + x*k
				best, bv := 0, -1
				for c := 0; c < 5; c++ {
					if v := int(b[c*plane+i]); v > bv {
						best, bv = c, v
					}
				}
				bm[z*out+x] = byte(best)
			}
		}
		w.BiomeRes, w.Biome = out, bm
	case "height": // uint16, (res)×(res), heights -500..500 m
		res := isqrt(len(b) / 2)
		out := 1024
		k := (res - 1) / out
		if k < 1 {
			k, out = 1, res
		}
		h := make([]float32, out*out)
		for z := 0; z < out; z++ {
			for x := 0; x < out; x++ {
				v := binary.LittleEndian.Uint16(b[2*((z*k)*res+x*k):])
				h[z*out+x] = float32(v)/32767*1000 - 500
			}
		}
		w.HeightRes, w.Height = out, h
	}
}
