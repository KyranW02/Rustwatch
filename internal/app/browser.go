package app

import (
	"os"
	"path/filepath"
	"strings"
)

// Browser is a browser RustWatch can drive for the Steam login.
type Browser struct {
	Name    string
	Path    string
	Firefox bool // Firefox-family (driven via WebDriver BiDi); otherwise Chromium-family (DevTools protocol)
}

var browserNames = map[string]string{
	"chrome.exe": "Google Chrome", "msedge.exe": "Microsoft Edge", "brave.exe": "Brave", "opera.exe": "Opera",
	"launcher.exe": "Opera", "vivaldi.exe": "Vivaldi", "chromium.exe": "Chromium", "browser.exe": "Yandex Browser",
	"firefox.exe": "Firefox", "librewolf.exe": "LibreWolf", "waterfox.exe": "Waterfox", "floorp.exe": "Floorp",
	"zen.exe": "Zen", "thorium.exe": "Thorium", "arc.exe": "Arc",
}

var firefoxFamily = map[string]bool{"firefox.exe": true, "librewolf.exe": true, "waterfox.exe": true, "floorp.exe": true, "zen.exe": true, "firefox": true}

// unsupported browsers can't be remote-controlled (Arc on Windows is Chromium but locks flags; IE etc.)
var unsupported = map[string]bool{"iexplore.exe": true, "arc.exe": true}

func browserFromPath(p string) (Browser, bool) {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(p, `\`, "/")))
	if base == "" || unsupported[base] {
		return Browser{}, false
	}
	name := browserNames[base]
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(strings.ReplaceAll(p, `\`, "/")), ".exe")
	}
	if base == "launcher.exe" && strings.Contains(strings.ToLower(p), "opera gx") {
		name = "Opera GX"
	}
	return Browser{Name: name, Path: p, Firefox: firefoxFamily[base]}, true
}

// exeFromCommand extracts the program path from a registry open command like
// "C:\Program Files\Mozilla Firefox\firefox.exe" -osint -url "%1"
func exeFromCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, `"`) {
		if end := strings.Index(cmd[1:], `"`); end >= 0 {
			return cmd[1 : end+1]
		}
	}
	low := strings.ToLower(cmd)
	if i := strings.Index(low, ".exe"); i >= 0 {
		return cmd[:i+4]
	}
	return strings.Fields(cmd + " ")[0]
}

// regValue pulls a value out of `reg query` output. An empty name returns the first value
// (the default value from `/ve`, whose label is localised on non-English Windows).
func regValue(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		i := strings.Index(line, "    REG_")
		if i < 0 {
			continue
		}
		label := strings.TrimSpace(line[:i])
		if name != "" && !strings.EqualFold(label, name) {
			continue
		}
		rest := strings.TrimSpace(line[i:])
		if sp := strings.Index(rest, " "); sp >= 0 {
			return strings.TrimSpace(rest[sp:])
		}
		return ""
	}
	return ""
}

// pickLoginBrowser returns the browser to use: the default browser when it can be driven,
// otherwise the first installed one that can. note explains a fallback.
func pickLoginBrowser() (b Browser, note string, err error) {
	def, ok := defaultBrowser()
	if ok {
		if _, statErr := statFile(def.Path); statErr == nil {
			return def, "", nil
		}
	}
	for _, c := range installedBrowsers() {
		if ok {
			note = "Your default browser can't be used for this, so RustWatch opened " + c.Name + " instead."
		}
		return c, note, nil
	}
	return Browser{}, "", errNoBrowser
}

func statFile(p string) (bool, error) {
	_, err := osStat(p)
	return err == nil, err
}

var osStat = os.Stat
