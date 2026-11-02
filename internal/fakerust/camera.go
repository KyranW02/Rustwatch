package fakerust

import (
	"math"
	"time"

	"rustwatch/internal/pb"
	"rustwatch/internal/rustplus"
	"rustwatch/internal/ws"
)

// Fake Rust+ cameras: BASECAM (CCTV), PTZ1 (pan/tilt/zoom), TURRET1 (auto turret), DRONE1.
// They stream a simple scene (ground, sky, a few buildings) that turns as you look around.

type fakeCam struct {
	flags      int32
	yaw, pitch float64
	zoom       int
}

const camW, camH = 160, 90

func (s *Server) camerasInit() {
	s.cams = map[string]*fakeCam{
		"BASECAM": {flags: 0, yaw: 30},
		"PTZ1":    {flags: rustplus.CamMouse, yaw: 200},
		"TURRET1": {flags: rustplus.CamMouse | rustplus.CamFire | rustplus.CamReload | rustplus.CamCrosshair, yaw: 120},
		"DRONE1":  {flags: rustplus.CamMovement | rustplus.CamMouse | rustplus.CamSprintAndDuck, yaw: 300},
	}
	s.camOrder = rustplus.SampleOrder(camW, camH)
}

// camRequest handles subscribe / unsubscribe / input. Caller holds s.mu.
func (s *Server) camRequest(conn *ws.Conn, kind int, sub []byte, resp *pb.Writer) {
	switch kind {
	case 30:
		var id string
		pb.Each(sub, func(f pb.Field) {
			if f.Num == 1 {
				id = f.Str()
			}
		})
		c := s.cams[id]
		if c == nil {
			e := &pb.Writer{}
			e.String(1, "no_camera")
			resp.Message(5, e)
			return
		}
		ci := &pb.Writer{}
		ci.Int(1, camW)
		ci.Int(2, camH)
		ci.Float(3, 0)
		ci.Float(4, 150)
		ci.Int(5, int64(c.flags))
		resp.Message(20, ci)
		if s.camSub[conn] != id {
			s.camSub[conn] = id
			go s.streamCamera(conn, id)
		}
	case 31:
		delete(s.camSub, conn)
		resp.Message(4, &pb.Writer{})
	case 32:
		id := s.camSub[conn]
		c := s.cams[id]
		if c == nil {
			e := &pb.Writer{}
			e.String(1, "no_camera")
			resp.Message(5, e)
			return
		}
		var buttons int64
		var dx, dy float64
		pb.Each(sub, func(f pb.Field) {
			switch f.Num {
			case 1:
				buttons = int64(f.V)
			case 2:
				pb.Each(f.B, func(g pb.Field) {
					switch g.Num {
					case 1:
						dx = float64(g.Float())
					case 2:
						dy = float64(g.Float())
					}
				})
			}
		})
		c.yaw = math.Mod(c.yaw+dx*0.5+360, 360)
		c.pitch = math.Max(-25, math.Min(25, c.pitch+dy*0.3))
		if c.flags&rustplus.CamMovement != 0 {
			if buttons&rustplus.BtnLeft != 0 {
				c.yaw = math.Mod(c.yaw-4+360, 360)
			}
			if buttons&rustplus.BtnRight != 0 {
				c.yaw = math.Mod(c.yaw+4, 360)
			}
		}
		if c.flags == rustplus.CamMouse && buttons&rustplus.BtnFirePrimary != 0 {
			c.zoom = (c.zoom + 1) % 4
		}
		resp.Message(4, &pb.Writer{})
	}
}

// scene returns distance (0..1023), alignment (0..63) and material for a pixel; py 0 is the bottom row.
func camScene(c fakeCam, px, py int) (int, int, int) {
	fov := 80.0 / float64(1+c.zoom)
	ang := math.Mod(c.yaw+(float64(px)-camW/2)*fov/camW+720, 360)
	up := (float64(py)-camH*0.42)*fov/camW + c.pitch
	type bld struct{ from, to, height, dist float64 }
	for _, b := range []bld{{15, 48, 14, 260}, {100, 112, 22, 420}, {118, 150, 9, 200}, {205, 260, 18, 330}, {300, 318, 30, 600}} {
		if ang >= b.from && ang <= b.to && up >= 0 && up <= b.height*30/b.dist*3 {
			al := 40 + int(20*math.Cos((ang-b.from)/(b.to-b.from)*math.Pi))
			return int(b.dist), al, 3
		}
	}
	if up < 0 {
		d := math.Min(1023, 1.6/math.Max(0.01, -up)*120)
		return int(d), 50, 5
	}
	return 1023, 0, 0
}

func (s *Server) streamCamera(conn *ws.Conn, id string) {
	off := 0
	t := time.NewTicker(80 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		if s.camSub[conn] != id || !s.conns[conn] {
			s.mu.Unlock()
			return
		}
		c := *s.cams[id]
		order := s.camOrder
		s.mu.Unlock()
		const per = 2400
		samples := make([][3]int, per)
		for k := 0; k < per; k++ {
			i := (off + k) % (camW * camH)
			x, y := int(order[2*i]), int(order[2*i+1])
			d, al, m := camScene(c, x, y)
			samples[k] = [3]int{d, al, m}
		}
		rays := &pb.Writer{}
		rays.Float(1, 65)
		rays.Int(2, int64(off))
		rays.BytesField(3, rustplus.EncodeRays(samples))
		rays.Float(4, 150)
		if id == "TURRET1" {
			e := &pb.Writer{}
			e.Uint(1, 9001)
			e.Uint(2, 2)
			v := &pb.Writer{}
			v.Float(1, 0)
			v.Float(2, 0)
			v.Float(3, 20)
			e.Message(3, v)
			e.Message(4, v)
			e.Message(5, v)
			e.String(6, "night_raider")
			rays.Message(5, e)
		}
		off = (off + per) % (camW * camH)
		b := &pb.Writer{}
		b.Message(10, rays)
		s.send(conn, 2, b)
	}
}
