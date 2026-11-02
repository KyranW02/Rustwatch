// Package fakerust is a simulated Rust+ server used for tests and the app's demo mode.
package fakerust

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"net"
	"net/http"
	"sync"
	"time"

	"rustwatch/internal/pb"
	"rustwatch/internal/ws"
)

type member struct {
	id     uint64
	name   string
	x, y   float32
	online bool
	alive  bool
	death  uint32
}

type sellOrder struct{ item, qty, cur, cost, stock int32 }

type vm struct {
	id     uint32
	x, y   float32
	name   string
	orders []sellOrder
}

type Server struct {
	mu              sync.Mutex
	MapSize         uint32
	members         []*member
	vms             []*vm
	switches        map[uint32]bool
	alarms          map[uint32]bool
	conns           map[*ws.Conn]bool
	start           time.Time
	chat            [][]byte
	Events          bool // simulate cargo / heli / chinook
	CensorNames     bool // query port behaves like server.censorplayerlist
	TeamMarkersOnly bool // like Rust since Aug 2026: Rust+ gets player markers only
	jpg             []byte
	listener        net.Listener
	Addr            string
	cams            map[string]*fakeCam
	camSub          map[*ws.Conn]string
	camOrder        []int16
}

func New() *Server {
	s := &Server{
		MapSize:  3500,
		switches: map[uint32]bool{1001: false, 1002: true},
		alarms:   map[uint32]bool{2001: false},
		conns:    map[*ws.Conn]bool{},
		start:    time.Now(),
		Events:   true,
	}
	s.members = []*member{
		{id: 76561198000000001, name: "You", x: 1200, y: 2100, online: true, alive: true},
		{id: 76561198000000002, name: "Bob", x: 1250, y: 2080, online: true, alive: true},
		{id: 76561198000000003, name: "Alice", x: 1210, y: 2150, online: false, alive: true},
	}
	s.vms = []*vm{
		{id: 501, x: 1750, y: 1750, name: "Outpost Shop", orders: []sellOrder{{1545779598, 1, -932201673, 450, 2}, {-1211166256, 128, -932201673, 40, 10}}},
		{id: 502, x: 900, y: 2600, name: "Bandit Deals", orders: []sellOrder{{-932201673, 500, 69511070, 1000, 5}, {1079279582, 1, -932201673, 25, 4}}},
	}
	shopNames := []string{"Scrap Shack", "Bob's Boom", "Farm Fresh", "Comps R Us", "Night Market", "Sulfur Stop", "The Armoury", "Cheap Wood", "Meds & More", "Clothing Co", "Outpost Gate", "Fishing Hut"}
	items := [][3]int32{{-151838493, 1000, 10}, {-1581843485, 1000, 50}, {69511070, 500, 25}, {317398316, 20, 100}, {-932201673, 100, 0}, {1545779598, 1, 500}, {-2002277461, 1, 120}, {1079279582, 2, 30}, {-1021495308, 1, 20}}
	for i, n := range shopNames {
		v := &vm{id: uint32(600 + i), name: n, x: float32(900 + (i*397)%1900), y: float32(800 + (i*613)%1900)}
		for j := 0; j < 1+i%3; j++ {
			it := items[(i+j*4)%len(items)]
			stock := int32((i + j) % 5)
			v.orders = append(v.orders, sellOrder{it[0], it[1], -932201673, it[2] + int32(i), stock})
		}
		s.vms = append(s.vms, v)
	}
	s.jpg = makeMap()
	s.camSub = map[*ws.Conn]string{}
	s.camerasInit()
	return s
}

func makeMap() []byte {
	const W = 700
	img := image.NewRGBA(image.Rect(0, 0, W, W))
	for y := 0; y < W; y++ {
		for x := 0; x < W; x++ {
			dx, dy := float64(x-W/2), float64(y-W/2)
			d := math.Sqrt(dx*dx+dy*dy) + 18*math.Sin(float64(x)/23) + 14*math.Cos(float64(y)/31)
			switch {
			case d < 230:
				g := uint8(110 + int(20*math.Sin(float64(x*y)/900)))
				img.Set(x, y, color.RGBA{70, g, 60, 255})
			case d < 245:
				img.Set(x, y, color.RGBA{196, 180, 130, 255})
			default:
				img.Set(x, y, color.RGBA{38, 70, 92, 255})
			}
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	return buf.Bytes()
}

// Start listens on addr ("127.0.0.1:0" for random).
func (s *Server) Start(addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.listener = l
	s.Addr = l.Addr().String()
	go http.Serve(l, http.HandlerFunc(s.handle))
	go s.simulate()
	return nil
}

func (s *Server) Close() { s.listener.Close() }

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Sec-WebSocket-Key")
	hj, ok := w.(http.Hijacker)
	if !ok || key == "" {
		http.Error(w, "expected websocket", 400)
		return
	}
	nc, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	fmt.Fprintf(nc, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", ws.AcceptKey(key))
	conn := ws.NewServerConn(nc, bufio.NewReader(brw))
	s.mu.Lock()
	s.conns[conn] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		delete(s.camSub, conn)
		s.mu.Unlock()
		conn.Close()
	}()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		s.onRequest(conn, data)
	}
}

func (s *Server) send(conn *ws.Conn, field int, body *pb.Writer) {
	w := &pb.Writer{}
	w.Message(field, body)
	conn.WriteServer(ws.OpBinary, w.Bytes())
}

func (s *Server) broadcast(body *pb.Writer) {
	s.mu.Lock()
	var cs []*ws.Conn
	for c := range s.conns {
		cs = append(cs, c)
	}
	s.mu.Unlock()
	for _, c := range cs {
		s.send(c, 2, body)
	}
}

func (s *Server) onRequest(conn *ws.Conn, data []byte) {
	var seq uint64
	var entity uint32
	var kind int
	var sub []byte
	pb.Each(data, func(f pb.Field) {
		switch f.Num {
		case 1:
			seq = f.V
		case 4:
			entity = uint32(f.V)
		case 2, 3:
		default:
			kind, sub = f.Num, f.B
		}
	})
	resp := &pb.Writer{}
	resp.Uint(1, seq)
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		s.send(conn, 1, resp)
	}()
	switch kind {
	case 8: // info
		w := &pb.Writer{}
		w.String(1, "[EU] RustWatch Demo Server | Monthly")
		w.String(2, "")
		w.String(3, "https://example.com")
		w.String(4, "Procedural Map")
		w.Uint(5, uint64(s.MapSize))
		w.Uint(6, uint64(s.start.Add(-26*time.Hour).Unix()))
		w.Uint(7, 147)
		w.Uint(8, 200)
		w.Uint(9, 3)
		resp.Message(6, w)
	case 9: // time
		w := &pb.Writer{}
		w.Float(1, 60)
		w.Float(2, 1)
		w.Float(3, 7)
		w.Float(4, 19.5)
		el := time.Since(s.start).Minutes()
		w.Float(5, float32(math.Mod(10+el*24/60, 24)))
		resp.Message(7, w)
	case 10: // map
		w := &pb.Writer{}
		w.Uint(1, 700)
		w.Uint(2, 700)
		w.BytesField(3, s.jpg)
		w.Int(4, 0)
		mons := []struct {
			t    string
			x, y float32
		}{{"outpost", 1750, 1750}, {"oil_rig_small", 3300, 1800}, {"large_oil_rig", 200, 1500}, {"launchsite", 2300, 900}, {"harbor_display_name", 1000, 450}, {"airfield_display_name", 1300, 2500}, {"bandit_camp", 900, 2600}}
		for _, m := range mons {
			mw := &pb.Writer{}
			mw.String(1, m.t)
			mw.Float(2, m.x)
			mw.Float(3, m.y)
			w.Message(5, mw)
		}
		resp.Message(8, w)
	case 11: // team info
		resp.Message(9, s.teamInfo())
	case 12: // team chat
		w := &pb.Writer{}
		for _, c := range s.chat {
			w.BytesField(1, c)
		}
		resp.Message(10, w)
	case 13: // send team message
		var msg string
		pb.Each(sub, func(f pb.Field) {
			if f.Num == 1 {
				msg = f.Str()
			}
		})
		resp.Message(4, &pb.Writer{})
		m := &pb.Writer{}
		m.Uint(1, s.members[0].id)
		m.String(2, s.members[0].name)
		m.String(3, msg)
		m.String(4, "#5af")
		m.Uint(5, uint64(time.Now().Unix()))
		s.chat = append(s.chat, m.Bytes())
		go func() {
			nm := &pb.Writer{}
			nm.Message(1, m)
			b := &pb.Writer{}
			b.Message(5, nm)
			s.broadcast(b)
		}()
	case 14: // entity info
		w := &pb.Writer{}
		p := &pb.Writer{}
		if v, ok := s.switches[entity]; ok {
			w.Uint(1, 1)
			p.Bool(1, v)
		} else if v, ok := s.alarms[entity]; ok {
			w.Uint(1, 2)
			p.Bool(1, v)
		} else if entity == 3001 {
			w.Uint(1, 3)
			for _, it := range [][2]int32{{-151838493, 6200}, {-1581843485, 3000}, {69511070, 1200}} {
				iw := &pb.Writer{}
				iw.Int(1, int64(it[0]))
				iw.Int(2, int64(it[1]))
				iw.Bool(3, false)
				p.Message(2, iw)
			}
			p.Int(3, 24)
			p.Bool(4, true)
			p.Uint(5, uint64(time.Now().Add(31*time.Hour).Unix()))
		} else {
			e := &pb.Writer{}
			e.String(1, "not_found")
			resp.Message(5, e)
			return
		}
		w.Message(3, p)
		resp.Message(11, w)
	case 15: // set entity
		var v bool
		pb.Each(sub, func(f pb.Field) {
			if f.Num == 1 {
				v = f.Bool()
			}
		})
		if _, ok := s.switches[entity]; !ok {
			e := &pb.Writer{}
			e.String(1, "not_found")
			resp.Message(5, e)
			return
		}
		s.switches[entity] = v
		resp.Message(4, &pb.Writer{})
		go func() {
			ec := &pb.Writer{}
			ec.Uint(1, uint64(entity))
			p := &pb.Writer{}
			p.Bool(1, v)
			ec.Message(2, p)
			b := &pb.Writer{}
			b.Message(6, ec)
			s.broadcast(b)
		}()
	case 18: // markers
		resp.Message(13, s.markers())
	case 30, 31, 32: // cameras
		s.camRequest(conn, kind, sub, resp)
	case 20: // promote
		var id uint64
		pb.Each(sub, func(f pb.Field) {
			if f.Num == 1 {
				id = f.V
			}
		})
		_ = id
		resp.Message(4, &pb.Writer{})
	default:
		e := &pb.Writer{}
		e.String(1, "unhandled")
		resp.Message(5, e)
	}
}

func (s *Server) teamInfo() *pb.Writer {
	w := &pb.Writer{}
	w.Uint(1, s.members[0].id)
	for _, m := range s.members {
		mw := &pb.Writer{}
		mw.Uint(1, m.id)
		mw.String(2, m.name)
		mw.Float(3, m.x)
		mw.Float(4, m.y)
		mw.Bool(5, m.online)
		mw.Uint(6, uint64(s.start.Unix()))
		mw.Bool(7, m.alive)
		mw.Uint(8, uint64(m.death))
		w.Message(2, mw)
	}
	death := &pb.Writer{}
	death.Int(2, 0)
	death.Float(3, 1320)
	death.Float(4, 1980)
	w.Message(3, death)
	pin := &pb.Writer{}
	pin.Int(2, 1)
	pin.Float(3, 2050)
	pin.Float(4, 2350)
	pin.Int(5, 1)
	pin.Int(6, 2)
	pin.String(7, "raid target")
	w.Message(3, pin)
	return w
}

func (s *Server) markers() *pb.Writer {
	w := &pb.Writer{}
	add := func(id uint32, typ int, x, y float32, f func(m *pb.Writer)) {
		m := &pb.Writer{}
		m.Uint(1, uint64(id))
		m.Uint(2, uint64(typ))
		m.Float(3, x)
		m.Float(4, y)
		if f != nil {
			f(m)
		}
		w.Message(1, m)
	}
	for i, m := range s.members {
		if m.online {
			mid := m.id
			rot := float32(i*110 + int(time.Since(s.start).Seconds())*3)
			add(uint32(100+i), 1, m.x, m.y, func(mm *pb.Writer) { mm.Uint(5, mid); mm.Float(6, rot) })
		}
	}
	if s.TeamMarkersOnly {
		return w
	}
	for _, v := range s.vms {
		vv := v
		add(v.id, 3, v.x, v.y, func(m *pb.Writer) {
			m.String(11, vv.name)
			for _, o := range vv.orders {
				so := &pb.Writer{}
				so.Int(1, int64(o.item))
				so.Int(2, int64(o.qty))
				so.Int(3, int64(o.cur))
				so.Int(4, int64(o.cost))
				so.Int(5, int64(o.stock))
				so.Bool(6, false)
				so.Bool(7, false)
				m.Message(13, so)
			}
		})
	}
	if s.Events {
		t := time.Since(s.start).Seconds()
		// cargo enters from the west after 15s and sails across
		if t > 15 && t < 600 {
			add(9001, 5, float32(-300+t*8), 1200, nil)
		}
		// patrol heli circles
		if t > 30 && t < 400 {
			a := t / 30
			add(9002, 8, float32(1750+900*math.Cos(a)), float32(1750+900*math.Sin(a)), nil)
		}
		// chinook flies to small oil rig at 60s
		if t > 60 && t < 120 {
			add(9003, 4, 3250, 1800, nil)
		}
		// an event radius (e.g. a monument event), red with alpha
		add(9005, 7, 2300, 900, func(m *pb.Writer) {
			m.Float(7, 120)
			c := &pb.Writer{}
			c.Float(1, 1)
			c.Float(2, 0.2)
			c.Float(3, 0.2)
			c.Float(4, 1)
			m.Message(8, c)
			m.Float(10, 0.3)
		})
		// travelling vendor
		if t > 20 {
			add(9004, 9, float32(1500+t), 1600, nil)
		}
	}
	return w
}

// simulate moves teammates and toggles a teammate online so events fire.
func (s *Server) simulate() {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	n := 0
	for range tick.C {
		n++
		s.mu.Lock()
		s.members[0].x += 5
		if n == 5 {
			s.members[2].online = true
		}
		changed := n == 5
		var ti *pb.Writer
		if changed {
			ti = s.teamInfo()
		}
		s.mu.Unlock()
		if changed {
			tc := &pb.Writer{}
			tc.Uint(1, s.members[0].id)
			tc.Message(2, ti)
			b := &pb.Writer{}
			b.Message(4, tc)
			s.broadcast(b)
		}
	}
}
