package app

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Where things live:
//   <data folder>\config.json            settings, servers, tracked players
//   <data folder>\logs\app-DATE.log       what RustWatch did (connections, pairing, errors)
//   <data folder>\logs\alerts-DATE.log    every alert
//   <data folder>\logs\chat-DATE.log      team chat
// The data folder defaults to Documents\RustWatch and can be moved in Settings.
// Browser profiles (login + app window) are large caches, so they stay in %LOCALAPPDATA%\RustWatch
// to keep them out of OneDrive / backup syncing.

func legacyDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "RustWatch")
}

// pointerFile remembers a custom data folder location.
func pointerFile() string {
	if d := legacyDir(); d != "" {
		return filepath.Join(d, "data-folder.txt")
	}
	return ""
}

func defaultDataDir() string {
	if d := documentsDir(); d != "" {
		return filepath.Join(d, "RustWatch")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "Documents", "RustWatch")
	}
	return "RustWatch"
}

// CacheDir is where browser profiles go.
func CacheDir() string {
	d, err := os.UserCacheDir() // %LOCALAPPDATA% on Windows
	if err != nil {
		return filepath.Join(os.TempDir(), "RustWatch")
	}
	return filepath.Join(d, "RustWatch")
}

// DataDir resolves the data folder and migrates config from the old hidden location.
func DataDir() string {
	dir := ""
	if pf := pointerFile(); pf != "" {
		if b, err := os.ReadFile(pf); err == nil {
			dir = strings.TrimSpace(string(b))
		}
	}
	if dir == "" {
		dir = defaultDataDir()
	}
	os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
	// one-time migration from %APPDATA%\RustWatch
	if old := legacyDir(); old != "" && !samePath(old, dir) {
		oldCfg, newCfg := filepath.Join(old, "config.json"), filepath.Join(dir, "config.json")
		if _, err := os.Stat(newCfg); os.IsNotExist(err) {
			if copyFile(oldCfg, newCfg) == nil {
				os.Rename(oldCfg, oldCfg+".moved")
			}
		}
		os.Remove(filepath.Join(old, "rustwatch.log"))
	}
	return dir
}

func samePath(a, b string) bool {
	ca, _ := filepath.Abs(a)
	cb, _ := filepath.Abs(b)
	return strings.EqualFold(filepath.Clean(ca), filepath.Clean(cb))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// MoveDataDir copies config and logs to newDir and remembers it. Takes effect after a restart.
func (a *App) MoveDataDir(newDir string) error {
	newDir = strings.Trim(strings.TrimSpace(newDir), `"`)
	if newDir == "" || !filepath.IsAbs(newDir) {
		return errors.New("enter a full folder path, e.g. D:\\Games\\RustWatch")
	}
	if samePath(newDir, a.dir) {
		return errors.New("that's already the data folder")
	}
	if err := os.MkdirAll(filepath.Join(newDir, "logs"), 0o755); err != nil {
		return fmt.Errorf("can't create that folder: %w", err)
	}
	probe := filepath.Join(newDir, ".rustwatch-write-test")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("can't write to that folder: %w", err)
	}
	os.Remove(probe)
	// flush current config, then copy everything we own
	a.mu.Lock()
	a.saveLocked()
	a.mu.Unlock()
	time.Sleep(300 * time.Millisecond)
	if err := copyFile(filepath.Join(a.dir, "config.json"), filepath.Join(newDir, "config.json")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("copy settings: %w", err)
	}
	if ents, err := os.ReadDir(filepath.Join(a.dir, "logs")); err == nil {
		for _, e := range ents {
			if !e.IsDir() {
				copyFile(filepath.Join(a.dir, "logs", e.Name()), filepath.Join(newDir, "logs", e.Name()))
			}
		}
	}
	pf := pointerFile()
	if pf == "" {
		return errors.New("can't save the new location")
	}
	os.MkdirAll(filepath.Dir(pf), 0o755)
	if err := os.WriteFile(pf, []byte(newDir), 0o644); err != nil {
		return err
	}
	log.Println("data folder moved to", newDir, "(old folder left in place:", a.dir+")")
	return nil
}

// ---------- dated log files ----------

type dailyLog struct {
	mu     sync.Mutex
	dir    string
	prefix string
	day    string
	f      *os.File
}

func newDailyLog(dir, prefix string) *dailyLog { return &dailyLog{dir: dir, prefix: prefix} }

func (l *dailyLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	day := time.Now().Format("2006-01-02")
	if l.f == nil || day != l.day {
		if l.f != nil {
			l.f.Close()
		}
		os.MkdirAll(l.dir, 0o755)
		f, err := os.OpenFile(filepath.Join(l.dir, l.prefix+"-"+day+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return 0, err
		}
		l.f, l.day = f, day
		pruneLogs(l.dir, l.prefix, 30)
	}
	return l.f.Write(p)
}

func (l *dailyLog) Printf(format string, args ...any) {
	fmt.Fprintf(l, time.Now().Format("15:04:05")+" "+format+"\n", args...)
}

// pruneLogs keeps the newest keep files for a prefix.
func pruneLogs(dir, prefix string, keep int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), prefix+"-") && strings.HasSuffix(e.Name(), ".log") {
			names = append(names, e.Name())
		}
	}
	for i := 0; i < len(names)-keep; i++ { // ReadDir is sorted; dates sort lexically
		os.Remove(filepath.Join(dir, names[i]))
	}
}

// AppLog returns the writer for the main application log in dir.
func AppLog(dir string) io.Writer { return newDailyLog(filepath.Join(dir, "logs"), "app") }
