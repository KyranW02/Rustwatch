package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

var quitOnce sync.Once

func (a *App) shutdown() {
	quitOnce.Do(func() {
		if a.tracker != nil && a.tracker.local != nil {
			a.tracker.local.Close() // let go of Steam before exiting
		}
		close(a.quit)
	})
}

// OpenWindow opens the app UI in a standalone window. When the last window closes the
// app exits, unless "keep running in background" is enabled in Settings.
func (a *App) OpenWindow() {
	url := fmt.Sprintf("http://127.0.0.1:%d/", a.Port)
	start := time.Now()
	cmd := OpenAppWindow(url, filepath.Join(CacheDir(), "window"))
	if cmd == nil {
		return
	}
	go func() {
		cmd.Wait()
		if time.Since(start) < 4*time.Second {
			return // handed off to an already-running window process
		}
		a.mu.Lock()
		keep := a.cfg.Settings.KeepRunning
		a.mu.Unlock()
		if !keep {
			a.shutdown()
		}
	}()
}

// SetBMBaseURL is used by demo mode / tests to point the tracker at a fake BattleMetrics.
func (a *App) SetBMBaseURL(u string) { a.tracker.baseURL = u }

// Dir is the data folder in use.
func (a *App) Dir() string { return a.dir }

// Restart relaunches RustWatch (used after moving the data folder).
func (a *App) Restart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return a.RestartWith(exe)
}

// RestartWith starts exe (e.g. a freshly installed update) and quits this instance.
func (a *App) RestartWith(exe string) error {
	var args []string
	for _, x := range os.Args[1:] {
		if x != "--after-restart" && x != "-after-restart" {
			args = append(args, x)
		}
	}
	cmd := exec.Command(exe, append(args, "--after-restart")...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		time.Sleep(400 * time.Millisecond)
		a.shutdown()
	}()
	return nil
}

// DemoSetup points Steam stats at the simulated API (demo mode only).
func (a *App) DemoSetup(steamBase string) {
	steamAPIBase = steamBase
	a.mu.Lock()
	a.cfg.Settings.SteamAPIKey = "demo"
	a.mu.Unlock()
}
