package a2s

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// modeServer: noSize = split packets without the size field; dropFirst = lose one packet of the
// first player reply; ignorePlayers = answer challenges but never send the player list.
func modeServer(t *testing.T, players int, noSize, dropFirst, ignorePlayers bool) string {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	ch := []byte{9, 8, 7, 6}
	var replies int32
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			req := buf[:n]
			if !bytes.HasSuffix(req, ch) {
				pc.WriteTo(append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x41}, ch...), addr)
				continue
			}
			if req[4] != 0x55 || ignorePlayers {
				continue
			}
			var b bytes.Buffer
			b.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x44, byte(players)})
			for i := 0; i < players; i++ {
				b.WriteByte(byte(i))
				fmt.Fprintf(&b, "Survivor %03d\x00", i)
				binary.Write(&b, binary.LittleEndian, int32(0))
				binary.Write(&b, binary.LittleEndian, math.Float32bits(float32(i)))
			}
			data := b.Bytes()
			const chunk = 700
			total := (len(data) + chunk - 1) / chunk
			first := atomic.AddInt32(&replies, 1) == 1
			for i := 0; i < total; i++ {
				if dropFirst && first && i == 1 {
					continue
				}
				var p bytes.Buffer
				p.Write([]byte{0xFE, 0xFF, 0xFF, 0xFF, 1, 0, 0, 0, byte(total), byte(i)})
				if !noSize {
					binary.Write(&p, binary.LittleEndian, uint16(1248))
				}
				p.Write(data[i*chunk : min((i+1)*chunk, len(data))])
				pc.WriteTo(p.Bytes(), addr)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func TestSplitWithoutSizeField(t *testing.T) {
	ps, err := QueryPlayers(modeServer(t, 200, true, false, false), time.Second)
	if err != nil || len(ps) != 200 || ps[199].Name != "Survivor 199" {
		t.Fatalf("%d %v", len(ps), err)
	}
}

func TestRetryAfterLostPacket(t *testing.T) {
	ps, err := QueryPlayers(modeServer(t, 200, false, true, false), 500*time.Millisecond)
	if err != nil || len(ps) != 200 {
		t.Fatalf("%d %v", len(ps), err)
	}
}

func TestServerIgnoresPlayerList(t *testing.T) {
	_, err := QueryPlayers(modeServer(t, 50, false, false, true), 300*time.Millisecond)
	if !errors.Is(err, ErrNoPlayerList) {
		t.Fatalf("got %v", err)
	}
}
