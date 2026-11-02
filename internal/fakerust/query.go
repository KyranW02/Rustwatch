package fakerust

import (
	"bytes"
	"encoding/binary"
	"math"
	"net"
	"time"
)

// StartQuery answers Steam A2S_INFO / A2S_PLAYER like a Rust server's query port.
func (s *Server) StartQuery(addr string) (string, error) {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return "", err
	}
	ch := []byte{7, 7, 7, 7}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			req := buf[:n]
			if n < 5 {
				continue
			}
			if !bytes.HasSuffix(req, ch) {
				pc.WriteTo(append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x41}, ch...), from)
				continue
			}
			on := s.onlinePlayers()
			declared := len(on)
			if s.CensorNames { // like server.censorplayerlist: stand-in names, and only part of the list
				stand := []string{"adrian", "alice", "aliza", "amada", "angelena", "ardelle", "assunta", "benito", "beula", "bree"}
				for i := range on {
					on[i].name = stand[i%len(stand)]
				}
				declared = len(on) + 120
				on = on[:len(on)/2]
			}
			var b bytes.Buffer
			b.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
			switch req[4] {
			case 0x54:
				b.Write([]byte{0x49, 17})
				b.WriteString("[EU] RustWatch Demo Server | Monthly\x00Procedural Map\x00rust\x00Rust\x00")
				b.Write([]byte{0, 0, byte(len(on)), 200, 0, 'd', 'w', 0, 1})
				b.WriteString("2400\x00\x30")
				binary.Write(&b, binary.LittleEndian, DemoServerSteamID)
				b.WriteString("mp200,cp147,qp3\x00")
			case 0x55:
				b.Write([]byte{0x44, byte(declared)})
				for i, p := range on {
					b.WriteByte(byte(i))
					b.WriteString(p.name + "\x00")
					binary.Write(&b, binary.LittleEndian, int32(0))
					binary.Write(&b, binary.LittleEndian, math.Float32bits(float32(p.dur.Seconds())))
				}
			default:
				continue
			}
			pc.WriteTo(b.Bytes(), from)
		}
	}()
	return pc.LocalAddr().String(), nil
}

// DemoServerSteamID is the Steam ID the demo server reports in its query reply.
const DemoServerSteamID uint64 = 90000000000000001

// DemoSteamIDs are the Steam IDs of the demo's online players, for the fake Steam client list.
func (s *Server) OnlineSteam() [][2]string {
	var out [][2]string
	for _, p := range s.onlinePlayers() {
		out = append(out, [2]string{"7656119800000" + p.id, p.name})
	}
	return out
}

type fakePlayer struct {
	id, name string
	dur      time.Duration
}

var demoPlayers = []struct {
	id, name  string
	from, to  float64
	sessStart float64 // seconds before demo start they connected
}{
	{"1001", "Bob", 0, 1e9, 3600}, {"1002", "xXSnipeKingXx", 0, 1e9, 7200}, {"1003", "Gary the Builder", 0, 45, 900},
	{"1004", "night_raider", 25, 1e9, 0}, {"1005", "Blueberry", 0, 1e9, 400}, {"1006", "Mr. Roof Camper", 0, 1e9, 12000},
	{"1007", "Larry", 0, 1e9, 60}, {"1008", "zerg_lord", 70, 1e9, 0}, {"1009", "Tom", 0, 1e9, 2400}, {"1010", "Kaiser", 0, 1e9, 5000},
}

func (s *Server) onlinePlayers() []fakePlayer {
	el := time.Since(s.start).Seconds()
	var out []fakePlayer
	for _, p := range demoPlayers {
		if el >= p.from && el < p.to {
			d := el - p.from
			if p.from == 0 {
				d += p.sessStart
			}
			out = append(out, fakePlayer{p.id, p.name, time.Duration(d * float64(time.Second))})
		}
	}
	return out
}
