// Package a2s implements the Steam server query protocol (A2S_INFO / A2S_PLAYER), which Rust
// servers answer on their query port. It's what server browsers and BattleMetrics use.
package a2s

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"time"
)

type Info struct {
	Name       string
	Map        string
	Players    int
	MaxPlayers int
	Bots       int
	Keywords   string
	SteamID    uint64 // the server's Steam ID (0 if it didn't send one)
}

type Player struct {
	Name     string
	Score    int32
	Duration time.Duration
}

var header = []byte{0xFF, 0xFF, 0xFF, 0xFF}

type conn struct {
	c       net.Conn
	timeout time.Duration
}

func dial(addr string, timeout time.Duration) (*conn, error) {
	c, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return nil, err
	}
	return &conn{c, timeout}, nil
}

// read returns one logical response, reassembling split packets.
func (c *conn) read() ([]byte, error) {
	buf := make([]byte, 65535)
	parts := map[int][]byte{}
	for {
		c.c.SetReadDeadline(time.Now().Add(c.timeout))
		n, err := c.c.Read(buf)
		if err != nil {
			if len(parts) > 0 {
				return nil, fmt.Errorf("%w (got %d split packets, some never arrived)", err, len(parts))
			}
			return nil, err
		}
		p := append([]byte(nil), buf[:n]...)
		if len(p) < 4 {
			continue
		}
		kind := int32(binary.LittleEndian.Uint32(p))
		if kind == -1 {
			return p[4:], nil
		}
		if kind != -2 || len(p) < 10 {
			continue
		}
		// split packet: id(4) total(1) number(1) [size(2)] payload
		if binary.LittleEndian.Uint32(p[4:])&0x80000000 != 0 {
			return nil, errors.New("compressed responses aren't supported")
		}
		total, num := int(p[8]), int(p[9])
		parts[num] = p
		if total > 0 && len(parts) >= total {
			// work out the header length from the first packet: its payload starts with FF FF FF FF
			off := 12
			if first := parts[0]; first != nil && len(first) >= 14 && bytes.Equal(first[10:14], header) {
				off = 10
			}
			var all []byte
			for i := 0; i < total; i++ {
				pk := parts[i]
				if pk == nil || len(pk) < off {
					return nil, errors.New("bad split packet")
				}
				all = append(all, pk[off:]...)
			}
			if len(all) >= 4 && int32(binary.LittleEndian.Uint32(all)) == -1 {
				return all[4:], nil
			}
			return all, nil
		}
	}
}

// request sends a query, following a challenge reply if the server asks for one.
func (c *conn) request(payload []byte, expect byte, challengeAppend bool) ([]byte, error) {
	send := append(append([]byte{}, header...), payload...)
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := c.c.Write(send); err != nil {
			return nil, err
		}
		resp, err := c.read()
		if err != nil {
			return nil, err
		}
		if len(resp) == 0 {
			continue
		}
		switch resp[0] {
		case expect:
			return resp[1:], nil
		case 0x41: // S2C_CHALLENGE
			if len(resp) < 5 {
				return nil, errors.New("bad challenge")
			}
			ch := resp[1:5]
			if challengeAppend {
				send = append(append(append([]byte{}, header...), payload...), ch...)
			} else {
				send = append(append(append([]byte{}, header...), payload[:1]...), ch...)
			}
		default:
			return nil, errors.New("unexpected response type")
		}
	}
	return nil, errors.New("server kept asking for a challenge")
}

type reader struct{ b *bytes.Reader }

func (r reader) str() string {
	var s []byte
	for {
		c, err := r.b.ReadByte()
		if err != nil || c == 0 {
			return string(s)
		}
		s = append(s, c)
	}
}
func (r reader) u8() byte { c, _ := r.b.ReadByte(); return c }
func (r reader) u16() uint16 {
	var v uint16
	binary.Read(r.b, binary.LittleEndian, &v)
	return v
}
func (r reader) i32() int32 {
	var v int32
	binary.Read(r.b, binary.LittleEndian, &v)
	return v
}
func (r reader) f32() float32 {
	var v uint32
	binary.Read(r.b, binary.LittleEndian, &v)
	return math.Float32frombits(v)
}

// QueryInfo asks a server for its name and player count.
func QueryInfo(addr string, timeout time.Duration) (*Info, error) {
	c, err := dial(addr, timeout)
	if err != nil {
		return nil, err
	}
	defer c.c.Close()
	resp, err := c.request(append([]byte{0x54}, []byte("Source Engine Query\x00")...), 0x49, true)
	if err != nil {
		return nil, err
	}
	r := reader{bytes.NewReader(resp)}
	r.u8() // protocol
	in := &Info{Name: r.str(), Map: r.str()}
	r.str() // folder
	r.str() // game
	r.u16() // app id
	in.Players = int(r.u8())
	in.MaxPlayers = int(r.u8())
	in.Bots = int(r.u8())
	r.u8()  // type
	r.u8()  // environment
	r.u8()  // visibility
	r.u8()  // vac
	r.str() // version
	if edf := r.u8(); edf != 0 {
		if edf&0x80 != 0 {
			r.u16()
		}
		if edf&0x10 != 0 {
			binary.Read(r.b, binary.LittleEndian, &in.SteamID)
		}
		if edf&0x40 != 0 {
			r.u16()
			r.str()
		}
		if edf&0x20 != 0 {
			in.Keywords = r.str()
		}
	}
	return in, nil
}

// ErrNoPlayerList means the server answered the challenge but never sent a player list —
// typical of servers behind DDoS protection that only answer basic server-browser queries.
var ErrNoPlayerList = errors.New("the server doesn't answer player-list requests")

// QueryPlayers returns everyone currently on the server with how long they've been connected.
// It retries a few times because a lost UDP packet in a big, split reply means starting over.
func QueryPlayers(addr string, timeout time.Duration) ([]Player, error) {
	l, err := QueryPlayerList(addr, timeout)
	return l.Players, err
}

// PlayerList is a player-list reply. Declared is how many players the server said it was
// sending; servers sometimes only fit part of the list in their reply.
type PlayerList struct {
	Players  []Player
	Declared int
}

// QueryPlayerList is QueryPlayers plus the server's declared player count.
func QueryPlayerList(addr string, timeout time.Duration) (PlayerList, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		l, err := queryPlayersOnce(addr, timeout+time.Duration(attempt)*2*time.Second)
		if err == nil {
			return l, nil
		}
		lastErr = err
		time.Sleep(time.Duration(300*(attempt+1)) * time.Millisecond)
	}
	var ne net.Error
	if errors.As(lastErr, &ne) && ne.Timeout() && !strings.Contains(lastErr.Error(), "split packets") {
		return PlayerList{}, ErrNoPlayerList
	}
	return PlayerList{}, lastErr
}

func queryPlayersOnce(addr string, timeout time.Duration) (PlayerList, error) {
	c, err := dial(addr, timeout)
	if err != nil {
		return PlayerList{}, err
	}
	defer c.c.Close()
	resp, err := c.request([]byte{0x55, 0xFF, 0xFF, 0xFF, 0xFF}, 0x44, false)
	if err != nil {
		return PlayerList{}, err
	}
	r := reader{bytes.NewReader(resp)}
	n := int(r.u8())
	out := make([]Player, 0, n)
	for i := 0; i < n && r.b.Len() > 0; i++ {
		r.u8() // index
		p := Player{Name: r.str(), Score: r.i32()}
		d := r.f32()
		if d > 0 && d < 1e7 {
			p.Duration = time.Duration(float64(d) * float64(time.Second))
		}
		out = append(out, p)
	}
	return PlayerList{Players: out, Declared: n}, nil
}
