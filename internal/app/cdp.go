package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"rustwatch/internal/ws"
)

// RustPlusLoginURL is the official Rust+ (companion app) Steam login page.
var RustPlusLoginURL = "https://companion-rust.facepunch.com/login"

// The Rust+ login page hands the auth token to the phone app by calling
// window.ReactNativeWebView.postMessage(JSON) as soon as Steam login completes.
// We define that object before any page script runs (DevTools "addScriptToEvaluateOnNewDocument"),
// so there is no race, and forward the message to RustWatch through a DevTools binding.
const injectScript = `(function(){
  function deliver(m){
    try { if (typeof window.rwToken === "function") { window.rwToken(String(m)); return; } } catch(e) {}
    try { var a = JSON.parse(m); location.href = "http://127.0.0.1:%d/pair/callback?token=" + encodeURIComponent(a.Token) + "&steamId=" + encodeURIComponent(a.SteamId || ""); } catch(e) {}
  }
  var bridge = { postMessage: deliver };
  try { Object.defineProperty(window, "ReactNativeWebView", { value: bridge, configurable: true }); } catch(e) { window.ReactNativeWebView = bridge; }
})();`

type cdpConn struct {
	c    *ws.Conn
	id   int
	msgs chan map[string]json.RawMessage
	err  chan error
}

func dialCDP(wsURL string) (*cdpConn, error) {
	c, err := ws.Dial(wsURL, 10*time.Second)
	if err != nil {
		return nil, err
	}
	cc := &cdpConn{c: c, msgs: make(chan map[string]json.RawMessage, 64), err: make(chan error, 1)}
	go func() {
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				cc.err <- err
				return
			}
			var m map[string]json.RawMessage
			if json.Unmarshal(data, &m) == nil {
				select {
				case cc.msgs <- m:
				default:
				}
			}
		}
	}()
	return cc, nil
}

func (cc *cdpConn) send(method string, params any) error {
	cc.id++
	b, _ := json.Marshal(map[string]any{"id": cc.id, "method": method, "params": params})
	return cc.c.WriteMessage(ws.OpText, b)
}

// launchError means the browser couldn't be driven at all, so another browser can be tried.
type launchError struct{ err error }

func (e launchError) Error() string { return e.err.Error() }

// browserLogin opens b (in its own profile) on the Rust+ login page and waits for the auth token.
func browserLogin(ctx context.Context, b Browser, profileDir string, callbackPort int, onStarted func(*exec.Cmd)) (string, string, error) {
	if b.Firefox {
		return bidiLogin(ctx, b.Path, profileDir, callbackPort, onStarted)
	}
	return cdpLogin(ctx, b.Path, profileDir, callbackPort, onStarted)
}

func parseAuth(payload string) (token, steamID string, ok bool) {
	var auth struct {
		SteamID json.RawMessage `json:"SteamId"`
		Token   string          `json:"Token"`
	}
	if err := json.Unmarshal([]byte(payload), &auth); err != nil || auth.Token == "" {
		log.Println("pairing: unexpected login message:", payload)
		return "", "", false
	}
	return auth.Token, strings.Trim(string(auth.SteamID), `"`), true
}

func cdpLogin(ctx context.Context, browser, profileDir string, callbackPort int, onStarted func(*exec.Cmd)) (token, steamID string, err error) {
	os.MkdirAll(profileDir, 0o755)
	portFile := filepath.Join(profileDir, "DevToolsActivePort")
	os.Remove(portFile)
	cmd := exec.Command(browser, "--remote-debugging-port=0", "--user-data-dir="+profileDir,
		"--no-first-run", "--no-default-browser-check", "--new-window", "--window-size=560,820", "about:blank")
	if err := cmd.Start(); err != nil {
		return "", "", fmt.Errorf("start browser: %w", err)
	}
	if onStarted != nil {
		onStarted(cmd)
	}
	go cmd.Wait()
	var cc *cdpConn
	defer func() {
		if cc != nil {
			cc.send("Browser.close", map[string]any{})
			time.Sleep(300 * time.Millisecond)
			cc.c.Close()
		}
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	}()

	// 1. wait for the DevTools port
	var port string
	for i := 0; i < 250 && port == ""; i++ {
		if b, err := os.ReadFile(portFile); err == nil {
			if lines := strings.SplitN(string(b), "\n", 2); len(lines) > 0 && strings.TrimSpace(lines[0]) != "" {
				port = strings.TrimSpace(lines[0])
			}
		}
		if port == "" {
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	if port == "" {
		return "", "", launchError{errors.New("the browser didn't allow RustWatch to connect")}
	}
	log.Println("pairing: devtools port", port)

	// 2. find the tab
	var wsURL string
	hc := &http.Client{Timeout: 3 * time.Second}
	for i := 0; i < 50 && wsURL == ""; i++ {
		resp, err := hc.Get("http://127.0.0.1:" + port + "/json/list")
		if err == nil {
			var targets []struct {
				Type, URL, WebSocketDebuggerURL string
			}
			json.NewDecoder(resp.Body).Decode(&targets)
			resp.Body.Close()
			for _, t := range targets {
				if t.Type == "page" && t.WebSocketDebuggerURL != "" {
					wsURL = t.WebSocketDebuggerURL
					break
				}
			}
		}
		if wsURL == "" {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if wsURL == "" {
		return "", "", launchError{errors.New("couldn't find the browser tab")}
	}
	cc, err = dialCDP(wsURL)
	if err != nil {
		return "", "", launchError{fmt.Errorf("devtools: %w", err)}
	}

	// 3. install the bridge and open the login page
	steps := []struct {
		m string
		p any
	}{
		{"Runtime.enable", map[string]any{}},
		{"Page.enable", map[string]any{}},
		{"Runtime.addBinding", map[string]any{"name": "rwToken"}},
		{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": fmt.Sprintf(injectScript, callbackPort)}},
		{"Page.navigate", map[string]any{"url": RustPlusLoginURL}},
	}
	for _, s := range steps {
		if err := cc.send(s.m, s.p); err != nil {
			return "", "", fmt.Errorf("devtools %s: %w", s.m, err)
		}
	}
	log.Println("pairing: login page opened")

	// 4. wait for the token
	for {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case err := <-cc.err:
			return "", "", fmt.Errorf("Steam login window was closed before finishing (%v)", err)
		case m := <-cc.msgs:
			var method string
			json.Unmarshal(m["method"], &method)
			if method == "Page.frameNavigated" {
				var p struct {
					Frame struct {
						URL string `json:"url"`
					} `json:"frame"`
				}
				json.Unmarshal(m["params"], &p)
				log.Println("pairing: navigated", p.Frame.URL)
			}
			if method != "Runtime.bindingCalled" {
				continue
			}
			var p struct{ Name, Payload string }
			json.Unmarshal(m["params"], &p)
			if p.Name != "rwToken" {
				continue
			}
			if tok, sid, ok := parseAuth(p.Payload); ok {
				log.Println("pairing: got Rust+ token for", sid)
				return tok, sid, nil
			}
		}
	}
}
