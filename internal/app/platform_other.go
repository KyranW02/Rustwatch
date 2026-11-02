//go:build !windows

package app

import (
	"os"
	"os/exec"
	"strings"
)

func findBrowser() string {
	for _, b := range []string{"microsoft-edge", "google-chrome", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	return ""
}

func OpenAppWindow(url, profileDir string) *exec.Cmd {
	if b := findBrowser(); b != "" {
		cmd := exec.Command(b, "--app="+url, "--user-data-dir="+profileDir, "--no-first-run", "--window-size=1440,920")
		if cmd.Start() == nil {
			return cmd
		}
	}
	exec.Command("xdg-open", url).Start()
	return nil
}

func defaultBrowser() (Browser, bool) {
	if p := os.Getenv("RUSTWATCH_BROWSER"); p != "" {
		b, ok := browserFromPath(p)
		b.Firefox = b.Firefox || strings.Contains(strings.ToLower(p), "firefox")
		return b, ok
	}
	return Browser{}, false
}

func installedBrowsers() []Browser {
	var out []Browser
	for _, n := range []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge", "brave-browser", "firefox"} {
		if p, err := exec.LookPath(n); err == nil {
			out = append(out, Browser{Name: n, Path: p, Firefox: n == "firefox"})
		}
	}
	return out
}

func documentsDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h + "/Documents"
	}
	return ""
}

func OpenFolder(dir string) error { return exec.Command("xdg-open", dir).Start() }
