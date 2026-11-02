package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionCompare(t *testing.T) {
	for _, c := range []struct {
		l, c string
		want bool
	}{{"v1.2.0", "1.1.0", true}, {"v1.1.0", "1.1.0", false}, {"1.0.9", "1.1.0", false}, {"v2.0", "1.9.9", true}, {"v1.10.0", "1.9.0", true}} {
		if got := newerVersion(c.l, c.c); got != c.want {
			t.Errorf("%s vs %s = %v", c.l, c.c, got)
		}
	}
}

func TestUpdateFlow(t *testing.T) {
	newExe := []byte("NEW RUSTWATCH BUILD")
	sum := sha256.Sum256(newExe)
	goodSum := hex.EncodeToString(sum[:])
	sumToServe := goodSum
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/KyranW02/rustwatch/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","body":"New stuff","html_url":"https://github.com/KyranW02/rustwatch/releases/tag/v9.9.9",
			"assets":[{"name":"RustWatch.exe","browser_download_url":"%[1]s/dl/exe","size":19},{"name":"RustWatch.exe.sha256","browser_download_url":"%[1]s/dl/sum"}]}`, srv.URL)
		case "/dl/exe":
			w.Write(newExe)
		case "/dl/sum":
			fmt.Fprintf(w, "%s  RustWatch.exe\n", sumToServe)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldAPI := githubAPI
	githubAPI = srv.URL
	defer func() { githubAPI = oldAPI }()
	dir := t.TempDir()
	cur := filepath.Join(dir, "RustWatch.exe")
	os.WriteFile(cur, []byte("OLD BUILD"), 0o755)
	exePath = func() (string, error) { return cur, nil }
	defer func() { exePath = os.Executable }()

	a := New(testDir(t))
	if err := a.CheckForUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !a.update.Available || a.update.Latest != "9.9.9" || a.update.Notes != "New stuff" {
		t.Fatalf("%+v", a.update)
	}
	if len(a.logs) == 0 || !strings.Contains(a.logs[len(a.logs)-1].Title, "9.9.9") {
		t.Fatal("no update alert")
	}

	// tampered download is refused and nothing is replaced
	sumToServe = strings.Repeat("0", 64)
	if _, err := a.InstallUpdate(context.Background()); err == nil || !strings.Contains(err.Error(), "doesn't match") {
		t.Fatalf("expected checksum failure, got %v", err)
	}
	if b, _ := os.ReadFile(cur); string(b) != "OLD BUILD" {
		t.Fatal("exe replaced despite bad checksum")
	}

	sumToServe = goodSum
	path, err := a.InstallUpdate(context.Background())
	if err != nil || path != cur {
		t.Fatal(path, err)
	}
	if b, _ := os.ReadFile(cur); string(b) != string(newExe) {
		t.Fatal("new exe not in place")
	}
	if b, _ := os.ReadFile(cur + ".old"); string(b) != "OLD BUILD" {
		t.Fatal("old exe not kept aside")
	}
	CleanupOldExe()
	if _, err := os.Stat(cur + ".old"); !os.IsNotExist(err) {
		t.Fatal("old exe not cleaned up")
	}
}
