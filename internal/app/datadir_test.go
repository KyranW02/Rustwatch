package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDataFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	legacy := filepath.Join(home, ".config", "RustWatch")
	os.MkdirAll(legacy, 0o755)
	os.WriteFile(filepath.Join(legacy, "config.json"), []byte(`{"settings":{"prefix":"."}}`), 0o644)

	dir := DataDir()
	if dir != filepath.Join(home, "Documents", "RustWatch") {
		t.Fatalf("default dir %s", dir)
	}
	a := New(dir)
	if a.cfg.Settings.Prefix != "." {
		t.Fatal("legacy config not migrated")
	}
	a.mu.Lock()
	a.alertLocked("cargo", "Cargo ship spawned", "at G5")
	a.mu.Unlock()
	day := time.Now().Format("2006-01-02")
	b, err := os.ReadFile(filepath.Join(dir, "logs", "alerts-"+day+".log"))
	if err != nil || !strings.Contains(string(b), "Cargo ship spawned") {
		t.Fatalf("alert log %q %v", b, err)
	}

	newDir := filepath.Join(home, "Games", "RW")
	if err := a.MoveDataDir(newDir); err != nil {
		t.Fatal(err)
	}
	if got := DataDir(); got != newDir {
		t.Fatalf("after move %s", got)
	}
	if _, err := os.Stat(filepath.Join(newDir, "logs", "alerts-"+day+".log")); err != nil {
		t.Fatal("logs not copied")
	}
	if b, _ := os.ReadFile(filepath.Join(newDir, "config.json")); !strings.Contains(string(b), `"prefix": "."`) {
		t.Fatalf("config not copied: %s", b)
	}
	if err := a.MoveDataDir("relative/path"); err == nil {
		t.Fatal("expected relative path error")
	}
}
