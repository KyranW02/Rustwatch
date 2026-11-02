package app

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"rustwatch/internal/fcm"
)

var errNoBrowser = errors.New("no supported browser found — install Chrome, Edge, Brave, Opera or Firefox")

type pairingSession struct {
	cmd    *exec.Cmd
	creds  *fcm.Credentials
	cancel context.CancelFunc
}

func (a *App) setFCMStatus(s string) {
	a.mu.Lock()
	a.fcmStatus = s
	a.mu.Unlock()
	a.publish("update", nil)
}

// loginCandidates: the default browser first, then other installed ones as fallbacks.
func loginCandidates() []Browser {
	var out []Browser
	seen := map[string]bool{}
	if b, ok := defaultBrowser(); ok {
		if _, err := os.Stat(b.Path); err == nil {
			out = append(out, b)
			seen[strings.ToLower(b.Path)] = true
		}
	}
	for _, b := range installedBrowsers() {
		if !seen[strings.ToLower(b.Path)] {
			out = append(out, b)
			seen[strings.ToLower(b.Path)] = true
		}
	}
	return out
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// StartSteamLink registers this PC as a Rust+ device and opens the Steam login in the
// user's default browser (in a separate RustWatch profile).
func (a *App) StartSteamLink() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	a.mu.Lock()
	if a.pairing != nil {
		a.mu.Unlock()
		cancel()
		return errors.New("Steam login is already open")
	}
	ps := &pairingSession{cancel: cancel}
	a.pairing = ps
	creds := a.cfg.FCM
	a.mu.Unlock()

	go func() {
		defer cancel()
		fail := func(err error) {
			log.Println("pairing failed:", err)
			a.mu.Lock()
			if a.pairing != ps { // already finished (e.g. via the callback page) or cancelled
				a.mu.Unlock()
				return
			}
			a.pairing = nil
			a.fcmStatus = "error: " + err.Error()
			a.mu.Unlock()
			a.publish("update", nil)
		}
		if creds == nil || creds.ExpoToken == "" {
			a.setFCMStatus("registering this PC with the Rust+ push service…")
			rctx, rcancel := context.WithTimeout(ctx, 60*time.Second)
			c, err := fcm.RegisterDevice(rctx)
			rcancel()
			if err != nil {
				fail(err)
				return
			}
			creds = c
		} else {
			cp := *creds
			creds = &cp
		}
		a.mu.Lock()
		ps.creds = creds
		a.mu.Unlock()

		cands := loginCandidates()
		if len(cands) == 0 {
			fail(errNoBrowser)
			return
		}
		var lastErr error
		for i, b := range cands {
			note := ""
			if i > 0 {
				note = " (" + cands[0].Name + " couldn't be used)"
			}
			a.setFCMStatus("Sign in with Steam in the " + b.Name + " window that just opened" + note + ".")
			log.Println("pairing: trying", b.Name, b.Path)
			profile := filepath.Join(CacheDir(), "steam-login", nonAlnum.ReplaceAllString(strings.ToLower(b.Name), "-"))
			tok, sid, err := browserLogin(ctx, b, profile, a.Port, func(c *exec.Cmd) {
				a.mu.Lock()
				ps.cmd = c
				a.mu.Unlock()
			})
			if err == nil {
				if err := a.FinishSteamLink(tok, sid); err != nil {
					fail(err)
				}
				return
			}
			lastErr = err
			var le launchError
			if !errors.As(err, &le) {
				break
			}
			log.Println("pairing:", b.Name, "couldn't be used:", err)
		}
		a.mu.Lock()
		done := a.pairing != ps
		a.mu.Unlock()
		if done {
			return
		}
		if ctx.Err() == context.Canceled {
			a.setFCMStatus("Steam login cancelled")
			a.mu.Lock()
			if a.pairing == ps {
				a.pairing = nil
			}
			a.mu.Unlock()
			a.publish("update", nil)
			return
		}
		fail(lastErr)
	}()
	return nil
}

// CancelSteamLink closes an open Steam login.
func (a *App) CancelSteamLink() {
	a.mu.Lock()
	ps := a.pairing
	a.mu.Unlock()
	if ps != nil && ps.cancel != nil {
		ps.cancel()
	}
}

// FinishSteamLink is called by the pairing page with the Rust+ auth token.
func (a *App) FinishSteamLink(token, steamID string) error {
	a.mu.Lock()
	ps := a.pairing
	a.mu.Unlock()
	if ps == nil || ps.creds == nil {
		return errors.New("no Steam login in progress — click 'Link Steam' in RustWatch first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := fcm.RegisterWithRustPlus(ctx, token, ps.creds.ExpoToken); err != nil {
		a.setFCMStatus("error: " + err.Error())
		return err
	}
	ps.creds.AuthToken = token
	ps.creds.SteamID = steamID
	a.mu.Lock()
	a.cfg.FCM = ps.creds
	a.pairing = nil
	a.saveLocked()
	a.startListenerLocked()
	a.alertLockedForce("Steam linked", "Now pair a server in Rust: ESC → Rust+ → Pair with Server")
	a.mu.Unlock()
	go func() {
		time.Sleep(1500 * time.Millisecond)
		if ps.cmd != nil && ps.cmd.Process != nil {
			ps.cmd.Process.Kill()
		}
	}()
	a.publish("update", nil)
	return nil
}

func (a *App) UnlinkSteam() {
	a.mu.Lock()
	if a.fcmCancel != nil {
		a.fcmCancel()
		a.fcmCancel = nil
	}
	a.cfg.FCM = nil
	a.fcmStatus = "not linked"
	a.saveLocked()
	a.mu.Unlock()
	a.publish("update", nil)
}
