//go:build windows

package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// regString reads a string value straight from the registry (no reg.exe).
func regString(root syscall.Handle, path, name string) string {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	var h syscall.Handle
	if syscall.RegOpenKeyEx(root, p, 0, syscall.KEY_READ, &h) != nil {
		return ""
	}
	defer syscall.RegCloseKey(h)
	var np *uint16
	if name != "" {
		np, _ = syscall.UTF16PtrFromString(name)
	}
	var typ, n uint32
	if syscall.RegQueryValueEx(h, np, nil, &typ, nil, &n) != nil || n < 2 {
		return ""
	}
	buf := make([]uint16, n/2+1)
	if syscall.RegQueryValueEx(h, np, nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &n) != nil {
		return ""
	}
	if typ != syscall.REG_SZ && typ != syscall.REG_EXPAND_SZ {
		return ""
	}
	s := syscall.UTF16ToString(buf)
	if typ == syscall.REG_EXPAND_SZ {
		s = expandWinEnv(s)
	}
	return s
}

func findBrowser() string {
	cands := []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("ProgramFiles"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("LOCALAPPDATA"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("ProgramFiles"), `Google\Chrome\Application\chrome.exe`),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), `Google\Chrome\Application\chrome.exe`),
		filepath.Join(os.Getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// OpenAppWindow opens the UI as a standalone app window (Edge/Chrome --app). Returns the process
// so the caller can wait for the window to close; nil if it fell back to the default browser.
func OpenAppWindow(url, profileDir string) *exec.Cmd {
	if b := findBrowser(); b != "" {
		cmd := exec.Command(b, "--app="+url, "--user-data-dir="+profileDir, "--no-first-run",
			"--no-default-browser-check", "--window-size=1440,920", "--disable-features=Translate")
		if err := cmd.Start(); err == nil {
			return cmd
		}
	}
	shellOpen(url)
	return nil
}

// defaultBrowser reads the user's default https handler from the registry.
func defaultBrowser() (Browser, bool) {
	progID := regString(syscall.HKEY_CURRENT_USER, `Software\Microsoft\Windows\Shell\Associations\UrlAssociations\https\UserChoice`, "ProgId")
	if progID == "" {
		progID = regString(syscall.HKEY_CURRENT_USER, `Software\Microsoft\Windows\Shell\Associations\UrlAssociations\http\UserChoice`, "ProgId")
	}
	if progID == "" {
		return Browser{}, false
	}
	cmd := regString(syscall.HKEY_CLASSES_ROOT, progID+`\shell\open\command`, "")
	if cmd == "" {
		return Browser{}, false
	}
	exe := expandWinEnv(exeFromCommand(cmd))
	return browserFromPath(exe)
}

func expandWinEnv(s string) string {
	for {
		i := strings.Index(s, "%")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i+1:], "%")
		if j < 0 {
			return s
		}
		name := s[i+1 : i+1+j]
		s = s[:i] + os.Getenv(name) + s[i+2+j:]
	}
}

// installedBrowsers lists drivable browsers in preference order (Chromium-family first).
func installedBrowsers() []Browser {
	pf, pf86, local := os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")
	cands := []string{
		filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(pf86, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(local, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(pf, `BraveSoftware\Brave-Browser\Application\brave.exe`),
		filepath.Join(local, `BraveSoftware\Brave-Browser\Application\brave.exe`),
		filepath.Join(local, `Programs\Opera GX\launcher.exe`),
		filepath.Join(local, `Programs\Opera\launcher.exe`),
		filepath.Join(local, `Vivaldi\Application\vivaldi.exe`),
		filepath.Join(pf86, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Mozilla Firefox\firefox.exe`),
		filepath.Join(pf86, `Mozilla Firefox\firefox.exe`),
	}
	var out []Browser
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			if b, ok := browserFromPath(c); ok {
				out = append(out, b)
			}
		}
	}
	return out
}

// documentsDir returns the real Documents folder (handles OneDrive / moved folders).
func documentsDir() string {
	v := regString(syscall.HKEY_CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Explorer\User Shell Folders`, "Personal")
	if v = expandWinEnv(v); v != "" {
		return v
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "Documents")
	}
	return ""
}

// OpenFolder shows a folder in Explorer.
func OpenFolder(dir string) error { return shellOpen(dir) }

var procShellExecute = syscall.NewLazyDLL("shell32.dll").NewProc("ShellExecuteW")

// shellOpen opens a URL or folder the normal Windows way (like double-clicking it).
func shellOpen(target string) error {
	verb, _ := syscall.UTF16PtrFromString("open")
	t, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r, _, _ := procShellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(t)), 0, 0, 1)
	if r <= 32 {
		return fmt.Errorf("couldn't open %s", target)
	}
	return nil
}
