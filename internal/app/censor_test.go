package app

import (
	"net"
	"strconv"
	"testing"
	"time"

	"rustwatch/internal/fakerust"
)

func TestCensoredPlayerList(t *testing.T) {
	if !isRandomName("Adrian") || isRandomName("xXSnipeKingXx") {
		t.Fatal("random-name list")
	}
	fs := fakerust.New()
	fs.CensorNames = true
	if err := fs.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	qaddr, _ := fs.StartQuery("127.0.0.1:0")
	a := New(testDir(t))
	host, p, _ := net.SplitHostPort(fs.Addr)
	port, _ := strconv.Atoi(p)
	a.ConnectServer(a.AddServer(ServerConfig{IP: host, Port: port, PlayerID: "1", PlayerToken: 1, QueryHint: qaddr}))
	go a.tracker.run()
	waitFor(t, "poll", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.tracker.status == "ok" })
	a.mu.Lock()
	v := a.tracker.viewLocked()
	a.mu.Unlock()
	if !v.Censored || v.Shown >= v.Total || v.Total < 100 {
		t.Fatalf("censored=%v shown=%d total=%d", v.Censored, v.Shown, v.Total)
	}
	a.Disconnect()
	time.Sleep(100 * time.Millisecond)
}
