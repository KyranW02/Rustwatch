package fcm

import (
	"net"
	"testing"

	"rustwatch/internal/pb"
)

func TestMCSFraming(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	dm := &pb.Writer{}
	dm.String(3, "976529667804")
	dm.String(5, "com.facepunch.rust.companion")
	kv := &pb.Writer{}
	kv.String(1, "body")
	kv.String(2, `{"type":"server","ip":"1.2.3.4","port":"28082","playerToken":"-5","playerId":"7656"}`)
	dm.Message(7, kv)
	kv2 := &pb.Writer{}
	kv2.String(1, "channelId")
	kv2.String(2, "pairing")
	dm.Message(7, kv2)
	dm.String(9, "0:123%abc")
	big := make([]byte, 300) // forces 2-byte varint size
	stream := append([]byte{41}, frame(3, []byte{10, 1, 'x'})...)
	stream = append(stream, frame(0, nil)...)
	stream = append(stream, frame(7, big)...)
	stream = append(stream, frame(8, dm.Bytes())...)
	go func() {
		for i := 0; i < len(stream); i += 7 { // trickle in small chunks
			end := i + 7
			if end > len(stream) {
				end = len(stream)
			}
			b.Write(stream[i:end])
		}
	}()
	r := &mcsReader{conn: a}
	v, _ := r.byte()
	if v != 41 {
		t.Fatal("version")
	}
	wantTags := []byte{3, 0, 7, 8}
	var last []byte
	for _, wt := range wantTags {
		tag, msg, err := r.message()
		if err != nil || tag != wt {
			t.Fatalf("tag %d want %d err %v", tag, wt, err)
		}
		last = msg
	}
	p := ParseDataMessage(last)
	if p.PersistentID != "0:123%abc" || p.Data["channelId"] != "pairing" || p.Data["body"] == "" {
		t.Fatalf("%+v", p)
	}
	if len(loginRequest(4567, 8910, []string{"x"})) < 50 {
		t.Fatal("login too short")
	}
}
