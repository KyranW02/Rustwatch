package fakerust

import (
	"context"
	"net"
	"strconv"
	"testing"

	"rustwatch/internal/rustplus"
)

func TestManyShops(t *testing.T) {
	s := New()
	for i := 0; i < 400; i++ {
		v := &vm{id: uint32(10000 + i), name: "Shop with a fairly long name number " + strconv.Itoa(i), x: float32(i * 9), y: float32(i * 7)}
		for j := 0; j < 8; j++ {
			v.orders = append(v.orders, sellOrder{int32(-151838493 + j), 1000, -932201673, 50, 10})
		}
		s.vms = append(s.vms, v)
	}
	if err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h, p, _ := net.SplitHostPort(s.Addr)
	port, _ := strconv.Atoi(p)
	c := &rustplus.Client{Host: h, Port: port, PlayerID: 1, PlayerToken: 1}
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	mk, raw, err := c.GetMapMarkersRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range mk {
		if m.Type == rustplus.MarkerVendingMachine && len(m.SellOrders) == 8 || m.Type == rustplus.MarkerVendingMachine {
			n++
		}
	}
	if n != 414 {
		t.Fatalf("got %d shops from %d bytes", n, len(raw))
	}
	t.Logf("%d shops in %d bytes", n, len(raw))
}
