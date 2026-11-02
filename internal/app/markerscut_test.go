package app

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"rustwatch/internal/fakerust"
)

// Since Rust's Aug 2026 update servers send Rust+ only team markers; RustWatch says so.
func TestMarkersCut(t *testing.T) {
	fs := fakerust.New()
	fs.TeamMarkersOnly = true
	if err := fs.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	a := New(testDir(t))
	host, p, _ := net.SplitHostPort(fs.Addr)
	port, _ := strconv.Atoi(p)
	a.ConnectServer(a.AddServer(ServerConfig{IP: host, Port: port, PlayerID: "76561198000000001", PlayerToken: 1}))
	waitFor(t, "markers", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.markerDiag.At != 0 })
	a.mu.Lock()
	cut := a.markersCutLocked()
	a.mu.Unlock()
	if !cut {
		t.Fatal("expected markers cut")
	}
	for cmd, want := range map[string]string{"buy ak": "Rust stopped sending vending machines", "cargo": "hasn't been heard yet"} {
		got := a.Command(76561198000000001, cmd)
		if !strings.Contains(got, want) {
			t.Errorf("%s: %q", cmd, got)
		}
	}
	a.Disconnect()
	time.Sleep(100 * time.Millisecond)
}
