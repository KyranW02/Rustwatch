package rustplus_test

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"rustwatch/internal/fakerust"
	"rustwatch/internal/pb"
	"rustwatch/internal/rustplus"
)

func TestNegativeInt32RoundTrip(t *testing.T) {
	w := &pb.Writer{}
	w.Int(1, -151838493)
	fs, err := pb.Parse(w.Bytes())
	if err != nil || fs[0].Int32() != -151838493 {
		t.Fatalf("got %v %v", fs, err)
	}
}

func TestClientAgainstFakeServer(t *testing.T) {
	s := fakerust.New()
	if err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	host, port, _ := net.SplitHostPort(s.Addr)
	p, _ := strconv.Atoi(port)
	got := make(chan *rustplus.Broadcast, 10)
	c := &rustplus.Client{Host: host, Port: p, PlayerID: 76561198000000001, PlayerToken: -12345,
		OnBroadcast: func(b *rustplus.Broadcast) { got <- b }}
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	info, err := c.GetInfo(ctx)
	if err != nil || info.MapSize != 3500 || info.Players != 147 {
		t.Fatalf("info %+v %v", info, err)
	}
	tm, err := c.GetTime(ctx)
	if err != nil || tm.Sunset != 19.5 {
		t.Fatalf("time %+v %v", tm, err)
	}
	m, err := c.GetMap(ctx)
	if err != nil || len(m.JPG) < 1000 || len(m.Monuments) != 7 {
		t.Fatalf("map %v %v", err, m.Monuments)
	}
	team, err := c.GetTeamInfo(ctx)
	if err != nil || len(team.Members) != 3 || team.Members[1].Name != "Bob" || !team.Members[0].Alive {
		t.Fatalf("team %+v %v", team, err)
	}
	mk, err := c.GetMapMarkers(ctx)
	if err != nil || len(mk) < 3 {
		t.Fatalf("markers %v %v", mk, err)
	}
	foundVM := false
	for _, x := range mk {
		if x.Type == rustplus.MarkerVendingMachine && len(x.SellOrders) == 2 && x.SellOrders[1].ItemID == -1211166256 {
			foundVM = true
		}
	}
	if !foundVM {
		t.Fatalf("vending not decoded: %+v", mk)
	}
	ei, err := c.GetEntityInfo(ctx, 3001)
	if err != nil || ei.Type != 3 || len(ei.Payload.Items) != 3 || ei.Payload.Items[0].ItemID != -151838493 {
		t.Fatalf("storage %+v %v", ei, err)
	}
	if _, err := c.GetEntityInfo(ctx, 42); err == nil {
		t.Fatal("expected not_found error")
	}
	if err := c.SetEntityValue(ctx, 1001, true); err != nil {
		t.Fatal(err)
	}
	if err := c.SendTeamMessage(ctx, "hello team"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	var sawEntity, sawMsg bool
	for !(sawEntity && sawMsg) {
		select {
		case b := <-got:
			if b.EntityChanged != nil && b.EntityChanged.EntityID == 1001 && b.EntityChanged.Payload.Value {
				sawEntity = true
			}
			if b.TeamMessage != nil && b.TeamMessage.Message == "hello team" {
				sawMsg = true
			}
		case <-deadline:
			t.Fatalf("broadcasts missing entity=%v msg=%v", sawEntity, sawMsg)
		}
	}
}
