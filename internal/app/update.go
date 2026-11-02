package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Updates come from GitHub Releases. A release is tagged like v1.2.0 and has two assets:
// RustWatch.exe and RustWatch.exe.sha256 (the exe's SHA-256). RustWatch only installs a
// download whose SHA-256 matches, and only when the user clicks Update.

// DefaultUpdateRepo is the GitHub "owner/repo" updates are read from. Set at build time with
// -ldflags "-X rustwatch/internal/app.DefaultUpdateRepo=owner/repo", or in Settings.
var DefaultUpdateRepo = "KyranW02/rustwatch"

var githubAPI = "https://api.github.com"

// exePath is the running exe (swappable in tests).
var exePath = os.Executable

type UpdateInfo struct {
	Current   string `json:"current"`
	Repo      string `json:"repo"`
	Available bool   `json:"available"`
	Latest    string `json:"latest"`
	Notes     string `json:"notes"`
	Page      string `json:"page"`
	Checked   int64  `json:"checked"`
	Error     string `json:"error"`
	Status    string `json:"status"` // "", "downloading", "installing"
	assetURL  string
	sumURL    string
	size      int64
}

func (a *App) updateRepoLocked() string {
	if r := strings.TrimSpace(a.cfg.Settings.UpdateRepo); r != "" {
		return r
	}
	return DefaultUpdateRepo
}

var semverRe = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

func parseVer(s string) [3]int {
	var v [3]int
	m := semverRe.FindStringSubmatch(s)
	for i := 1; m != nil && i <= 3; i++ {
		v[i-1], _ = strconv.Atoi(m[i])
	}
	return v
}

func newerVersion(latest, current string) bool {
	l, c := parseVer(latest), parseVer(current)
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

var updHTTP = &http.Client{Timeout: 30 * time.Second}

// CheckForUpdate asks GitHub for the latest release.
func (a *App) CheckForUpdate(ctx context.Context) error {
	a.mu.Lock()
	repo := a.updateRepoLocked()
	a.mu.Unlock()
	info := UpdateInfo{Current: Version, Repo: repo, Checked: time.Now().Unix()}
	defer func() {
		a.mu.Lock()
		prev := a.update
		info.Status = prev.Status
		a.update = info
		if info.Available && a.updateNotified != info.Latest {
			a.updateNotified = info.Latest
			a.alertLocked("update", "RustWatch "+info.Latest+" is available", "Open Settings → Updates (or the banner on Overview) to install it.")
		}
		a.mu.Unlock()
		a.publish("update", nil)
	}()
	if repo == "" {
		info.Error = "no update source set"
		return errors.New(info.Error)
	}
	if !regexp.MustCompile(`^[\w.-]+/[\w.-]+$`).MatchString(repo) {
		info.Error = "update source must look like owner/repo"
		return errors.New(info.Error)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", githubAPI+"/repos/"+repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "RustWatch/"+Version)
	resp, err := updHTTP.Do(req)
	if err != nil {
		info.Error = "couldn't reach GitHub: " + err.Error()
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		info.Error = "no releases found for " + repo
		return errors.New(info.Error)
	}
	if resp.StatusCode != 200 {
		info.Error = fmt.Sprintf("GitHub answered %d", resp.StatusCode)
		return errors.New(info.Error)
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Name   string `json:"name"`
		Body   string `json:"body"`
		Page   string `json:"html_url"`
		Draft  bool   `json:"draft"`
		Pre    bool   `json:"prerelease"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		info.Error = "unexpected reply from GitHub"
		return err
	}
	info.Latest, info.Notes, info.Page = strings.TrimPrefix(rel.Tag, "v"), strings.TrimSpace(rel.Body), rel.Page
	for _, as := range rel.Assets {
		switch strings.ToLower(as.Name) {
		case "rustwatch.exe":
			info.assetURL, info.size = as.URL, as.Size
		case "rustwatch.exe.sha256":
			info.sumURL = as.URL
		}
	}
	info.Available = !rel.Draft && !rel.Pre && newerVersion(rel.Tag, Version)
	if info.Available && (info.assetURL == "" || info.sumURL == "") {
		info.Available = false
		info.Error = fmt.Sprintf("release %s is missing RustWatch.exe or RustWatch.exe.sha256", rel.Tag)
	}
	return nil
}

func (a *App) updateLoop() {
	time.Sleep(20 * time.Second)
	for {
		a.mu.Lock()
		auto := !a.cfg.Settings.NoUpdateCheck && !a.Demo
		repo := a.updateRepoLocked()
		a.mu.Unlock()
		if auto && repo != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			if err := a.CheckForUpdate(ctx); err != nil {
				log.Println("update check:", err)
			}
			cancel()
		}
		time.Sleep(6 * time.Hour)
	}
}

func (a *App) setUpdateStatus(s string) {
	a.mu.Lock()
	a.update.Status = s
	a.mu.Unlock()
	a.publish("update", nil)
}

func download(ctx context.Context, url string, w io.Writer, limit int64) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "RustWatch/"+Version)
	c := &http.Client{Timeout: 10 * time.Minute}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed (%d)", resp.StatusCode)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err == nil && n > limit {
		return errors.New("download is unexpectedly large")
	}
	return err
}

// InstallUpdate downloads the latest exe, checks its SHA-256 and swaps it in. It returns the path
// of the new exe to start; the caller restarts.
func (a *App) InstallUpdate(ctx context.Context) (string, error) {
	a.mu.Lock()
	u := a.update
	a.mu.Unlock()
	if !u.Available {
		return "", errors.New("no update available — check again first")
	}
	cur, err := exePath()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(cur); err == nil {
		cur = p
	}
	dir := filepath.Dir(cur)
	a.setUpdateStatus("downloading")
	defer a.setUpdateStatus("")

	var sumBuf strings.Builder
	if err := download(ctx, u.sumURL, &sumBuf, 4096); err != nil {
		return "", fmt.Errorf("checksum: %w", err)
	}
	want := strings.ToLower(regexp.MustCompile(`[0-9a-fA-F]{64}`).FindString(sumBuf.String()))
	if want == "" {
		return "", errors.New("RustWatch.exe.sha256 doesn't contain a SHA-256")
	}
	newPath := filepath.Join(dir, ".RustWatch.update.exe")
	f, err := os.Create(newPath)
	if err != nil {
		return "", fmt.Errorf("can't write next to RustWatch.exe (%v) — move RustWatch to a folder you own, like Documents, or download it from the release page", err)
	}
	h := sha256.New()
	err = download(ctx, u.assetURL, io.MultiWriter(f, h), 200<<20)
	f.Close()
	if err != nil {
		os.Remove(newPath)
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(newPath)
		return "", fmt.Errorf("the download's SHA-256 doesn't match the release (got %s…), so it wasn't installed", got[:12])
	}
	a.setUpdateStatus("installing")
	// Windows lets a running exe be renamed but not overwritten: move the old one aside first.
	old := cur + ".old"
	os.Remove(old)
	if err := os.Rename(cur, old); err != nil {
		os.Remove(newPath)
		return "", fmt.Errorf("couldn't replace RustWatch.exe: %w", err)
	}
	if err := os.Rename(newPath, cur); err != nil {
		os.Rename(old, cur) // put the original back
		os.Remove(newPath)
		return "", fmt.Errorf("couldn't install the new version: %w", err)
	}
	os.Chmod(cur, 0o755)
	log.Printf("updated RustWatch %s -> %s", Version, u.Latest)
	return cur, nil
}

// CleanupOldExe removes the previous version left behind by an update.
func CleanupOldExe() {
	if cur, err := exePath(); err == nil {
		os.Remove(cur + ".old")
		os.Remove(filepath.Join(filepath.Dir(cur), ".RustWatch.update.exe"))
	}
}
