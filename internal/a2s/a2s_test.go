package a2s

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"testing"
	"time"
)

// fake server: requires challenges, splits the player list across packets.
func fakeServer(t *testing.T, players int) string {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	ch := []byte{1, 2, 3, 4}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			req := buf[:n]
			hasCh := bytes.HasSuffix(req, ch)
			switch req[4] {
			case 0x54:
				if !hasCh {
					pc.WriteTo(append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x41}, ch...), addr)
					continue
				}
				var b bytes.Buffer
				b.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x49, 17})
				b.WriteString("[EU] Test Server\x00Procedural Map\x00rust\x00Rust\x00")
				b.Write([]byte{0, 0, byte(players), 200, 0, 'd', 'w', 0, 1})
				b.WriteString("2400\x00")
				b.WriteByte(0x20)
				b.WriteString("mp200,cp147\x00")
				pc.WriteTo(b.Bytes(), addr)
			case 0x55:
				if !hasCh {
					pc.WriteTo(append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x41}, ch...), addr)
					continue
				}
				var b bytes.Buffer
				b.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x44, byte(players)})
				for i := 0; i < players; i++ {
					b.WriteByte(byte(i))
					fmt.Fprintf(&b, "Player number %03d\x00", i)
					binary.Write(&b, binary.LittleEndian, int32(i))
					binary.Write(&b, binary.LittleEndian, math.Float32bits(float32(60*i+5)))
				}
				data := b.Bytes()
				const chunk = 1000
				total := (len(data) + chunk - 1) / chunk
				for i := 0; i < total; i++ {
					end := min((i+1)*chunk, len(data))
					var p bytes.Buffer
					p.Write([]byte{0xFE, 0xFF, 0xFF, 0xFF, 9, 0, 0, 0, byte(total), byte(i)})
					binary.Write(&p, binary.LittleEndian, uint16(1248))
					p.Write(data[i*chunk : end])
					pc.WriteTo(p.Bytes(), addr)
				}
			}
		}
	}()
	return pc.LocalAddr().String()
}

func TestQuery(t *testing.T) {
	addr := fakeServer(t, 150)
	in, err := QueryInfo(addr, time.Second)
	if err != nil || in.Name != "[EU] Test Server" || in.Players != 150 || in.MaxPlayers != 200 || in.Keywords != "mp200,cp147" {
		t.Fatalf("%+v %v", in, err)
	}
	ps, err := QueryPlayers(addr, time.Second)
	if err != nil || len(ps) != 150 || ps[149].Name != "Player number 149" || ps[2].Duration != 125*time.Second {
		t.Fatalf("%d %v %+v", len(ps), err, ps[:2])
	}
}
