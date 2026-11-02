package rustplus

import (
	"context"
	"errors"
	"image"
	"image/color"

	"rustwatch/internal/pb"
)

// Rust+ cameras: CCTV, PTZ cameras, auto turrets and drones that were given an ID at a computer
// station. The server streams "rays" (a compressed depth/material image, a slice at a time) to
// a subscribed player. It only works while the paired player is NOT connected to the server.

// Camera control flags (what the subscribed camera supports).
const (
	CamMovement      = 1
	CamMouse         = 2
	CamSprintAndDuck = 4
	CamFire          = 8
	CamReload        = 16
	CamCrosshair     = 32
)

// Camera input buttons.
const (
	BtnForward     = 2
	BtnBackward    = 4
	BtnLeft        = 8
	BtnRight       = 16
	BtnJump        = 32
	BtnDuck        = 64
	BtnSprint      = 128
	BtnUse         = 256
	BtnFirePrimary = 1024
	BtnFireSecond  = 2048
	BtnReload      = 8192
)

type CameraInfo struct {
	Width, Height int32
	NearPlane     float32
	FarPlane      float32
	ControlFlags  int32
}

type Vec3 struct{ X, Y, Z float32 }

type CameraEntity struct {
	ID       uint32
	Type     int32 // 1 tree, 2 player
	Position Vec3
	Rotation Vec3
	Size     Vec3
	Name     string
}

type CameraRays struct {
	VerticalFov  float32
	SampleOffset int32
	RayData      []byte
	Distance     float32
	Entities     []CameraEntity
}

// CameraSubscribe starts (or renews: Rust+ wants a renewal every ~10 s) a camera stream.
func (c *Client) CameraSubscribe(ctx context.Context, id string) (*CameraInfo, error) {
	r, err := c.request(ctx, 1, 0, func(w *pb.Writer) {
		m := &pb.Writer{}
		m.String(1, id)
		w.Message(30, m)
	})
	if err != nil {
		return nil, err
	}
	if r.CameraInfo == nil {
		return nil, errors.New("the server didn't send camera details")
	}
	return r.CameraInfo, nil
}

func (c *Client) CameraUnsubscribe(ctx context.Context) error {
	_, err := c.request(ctx, 1, 0, empty(31))
	return err
}

// CameraInput sends held buttons and a mouse movement to the subscribed camera.
func (c *Client) CameraInput(ctx context.Context, buttons int32, dx, dy float32) error {
	_, err := c.request(ctx, 0.25, 0, func(w *pb.Writer) {
		m := &pb.Writer{}
		m.Int(1, int64(buttons))
		v := &pb.Writer{}
		v.Float(1, dx)
		v.Float(2, dy)
		m.Message(2, v)
		w.Message(32, m)
	})
	return err
}

func decodeCameraInfo(b []byte) *CameraInfo {
	ci := &CameraInfo{}
	pb.Each(b, func(f pb.Field) {
		switch f.Num {
		case 1:
			ci.Width = f.Int32()
		case 2:
			ci.Height = f.Int32()
		case 3:
			ci.NearPlane = f.Float()
		case 4:
			ci.FarPlane = f.Float()
		case 5:
			ci.ControlFlags = f.Int32()
		}
	})
	return ci
}

func decodeVec3(b []byte) Vec3 {
	var v Vec3
	pb.Each(b, func(f pb.Field) {
		switch f.Num {
		case 1:
			v.X = f.Float()
		case 2:
			v.Y = f.Float()
		case 3:
			v.Z = f.Float()
		}
	})
	return v
}

func decodeCameraRays(b []byte) *CameraRays {
	cr := &CameraRays{}
	pb.Each(b, func(f pb.Field) {
		switch f.Num {
		case 1:
			cr.VerticalFov = f.Float()
		case 2:
			cr.SampleOffset = f.Int32()
		case 3:
			cr.RayData = append([]byte(nil), f.B...)
		case 4:
			cr.Distance = f.Float()
		case 5:
			e := CameraEntity{}
			pb.Each(f.B, func(g pb.Field) {
				switch g.Num {
				case 1:
					e.ID = uint32(g.V)
				case 2:
					e.Type = g.Int32()
				case 3:
					e.Position = decodeVec3(g.B)
				case 4:
					e.Rotation = decodeVec3(g.B)
				case 5:
					e.Size = decodeVec3(g.B)
				case 6:
					e.Name = g.Str()
				}
			})
			cr.Entities = append(cr.Entities, e)
		}
	})
	return cr
}

// ---- frame decoding (same scheme as the official Rust+ app) ----

// indexGen is the xorshift generator the app uses to shuffle pixel order (seed 1337).
type indexGen struct{ state uint32 }

func newIndexGen(seed uint32) *indexGen {
	g := &indexGen{state: seed}
	g.next()
	return g
}

func (g *indexGen) next() float64 {
	old := g.state
	s := g.state
	s ^= s << 13
	s ^= s >> 17
	s ^= s << 5
	g.state = s
	if int32(old) >= 0 {
		return float64(old)
	}
	return float64(old) - 1
}

func (g *indexGen) nextInt(max int) int {
	return int(g.next() * float64(max) / 4294967295)
}

// SampleOrder is the pixel visiting order for a width×height camera: pairs of x, y.
func SampleOrder(width, height int) []int16 {
	n := width * height
	buf := make([]int16, 2*n)
	for y, w := 0, 0; y < height; y++ {
		for x := 0; x < width; x++ {
			buf[w], buf[w+1] = int16(x), int16(y)
			w += 2
		}
	}
	g := newIndexGen(1337)
	for r := n - 1; r >= 1; r-- {
		c := 2 * r
		i := 2 * g.nextInt(r+1)
		buf[c], buf[c+1], buf[i], buf[i+1] = buf[i], buf[i+1], buf[c], buf[c+1]
	}
	return buf
}

// Sample is one decoded camera pixel: distance 0..1023, surface alignment 0..63, material.
type Sample struct {
	Dist, Align, Mat uint16
	Set              bool
}

func lookbackIndex(t, r, i int) int { return (3*(t/128) + 5*(r/16) + 7*i) & 63 }

// DecodeRays writes the samples in one rays message into pix (indexed x + y*width).
func DecodeRays(rays *CameraRays, order []int16, width, height int, pix []Sample) {
	n := width * height
	if n == 0 || len(order) < 2*n || len(pix) < n {
		return
	}
	off := 2 * int(rays.SampleOffset)
	var look [64][3]int
	d := rays.RayData
	p := 0
	for p < len(d)-1 {
		var t, r, i int
		b := int(d[p])
		p++
		if b == 255 {
			if p+2 >= len(d) {
				return
			}
			l, o, s := int(d[p]), int(d[p+1]), int(d[p+2])
			p += 3
			t, r, i = l<<2|o>>6, o&63, s
			look[lookbackIndex(t, r, i)] = [3]int{t, r, i}
		} else {
			switch b & 192 {
			case 0:
				e := look[b&63]
				t, r, i = e[0], e[1], e[2]
			case 64:
				if p >= len(d) {
					return
				}
				e := look[b&63]
				g := int(d[p])
				p++
				t, r, i = e[0]+(g>>3-15), e[1]+(g&7-3), e[2]
			case 128:
				if p >= len(d) {
					return
				}
				e := look[b&63]
				t, r, i = e[0]+(int(d[p])-127), e[1], e[2]
				p++
			default:
				if p+1 >= len(d) {
					return
				}
				a1, f := int(d[p]), int(d[p+1])
				p += 2
				t, r, i = a1<<2|f>>6, f&63, b&63
				look[lookbackIndex(t, r, i)] = [3]int{t, r, i}
			}
		}
		off %= 2 * n
		idx := int(order[off]) + int(order[off+1])*width
		off += 2
		if idx >= 0 && idx < n {
			pix[idx] = Sample{Dist: uint16(clampInt(t, 0, 1023)), Align: uint16(clampInt(r, 0, 63)), Mat: uint16(i), Set: true}
		}
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

var materialColours = [8][3]float64{
	{0.5, 0.5, 0.5}, {0.8, 0.7, 0.7}, {0.3, 0.7, 1}, {0.6, 0.6, 0.6},
	{0.7, 0.7, 0.7}, {0.8, 0.6, 0.4}, {1, 0.4, 0.4}, {1, 0.1, 0.1},
}

// RenderFrame turns decoded samples into an image (row 0 of the samples is the bottom).
func RenderFrame(pix []Sample, width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			s := pix[x+y*width]
			var c color.RGBA
			switch {
			case !s.Set:
				c = color.RGBA{24, 23, 20, 255}
			case s.Dist == 1023 && s.Align == 0 && s.Mat == 0:
				c = color.RGBA{208, 230, 252, 255}
			default:
				m := materialColours[int(s.Mat)%8]
				al := float64(s.Align) / 63
				// fade with distance a little, so depth reads like the app's view
				fog := 1 - 0.35*float64(s.Dist)/1023
				k := al * fog * 255
				c = color.RGBA{uint8(m[0] * k), uint8(m[1] * k), uint8(m[2] * k), 255}
			}
			img.SetRGBA(x, height-1-y, c)
		}
	}
	return img
}

// EncodeRays packs samples as literal entries (fake server and tests).
func EncodeRays(samples [][3]int) []byte {
	var out []byte
	for _, s := range samples {
		t, r, i := s[0], s[1], s[2]
		out = append(out, 255, byte(t>>2), byte((t&3)<<6|r&63), byte(i))
	}
	return out
}
