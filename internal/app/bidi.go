package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"rustwatch/internal/ws"
)

// Firefox-family browsers are driven with WebDriver BiDi: a preload script defines
// window.ReactNativeWebView before the Rust+ page runs and passes the token over a channel.
const bidiPreload = `(send) => {
  const bridge = { postMessage: (m) => send(String(m)) };
  try { Object.defineProperty(window, "ReactNativeWebView", { value: bridge, configurable: true }); } catch (e) { window.ReactNativeWebView = bridge; }
}`

const firefoxPrefs = `user_pref("browser.shell.checkDefaultBrowser", false);
user_pref("browser.aboutwelcome.enabled", false);
user_pref("browser.startup.homepage_override.mstone", "ignore");
user_pref("datareporting.policy.dataSubmissionPolicyBypassNotification", true);
user_pref("toolkit.telemetry.reportingpolicy.firstRun", false);
user_pref("trailhead.firstrun.didSeeAboutWelcome", true);
user_pref("remote.active-protocols", 1);
`

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func bidiLogin(ctx context.Context, browser, profileDir string, callbackPort int, onStarted func(*exec.Cmd)) (string, string, error) {
	os.MkdirAll(profileDir, 0o755)
	os.WriteFile(filepath.Join(profileDir, "user.js"), []byte(firefoxPrefs), 0o644)
	port, err := freePort()
	if err != nil {
		return "", "", launchError{err}
	}
	cmd := exec.Command(browser, "--remote-debugging-port", strconv.Itoa(port), "-profile", profileDir,
		"-no-remote", "-new-instance", "about:blank")
	if err := cmd.Start(); err != nil {
		return "", "", launchError{fmt.Errorf("start browser: %w", err)}
	}
	if onStarted != nil {
		onStarted(cmd)
	}
	go cmd.Wait()
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	}()

	var c *ws.Conn
	url := fmt.Sprintf("ws://127.0.0.1:%d/session", port)
	for i := 0; i < 100; i++ {
		if c, err = ws.Dial(url, 2*time.Second); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if c == nil {
		return "", "", launchError{errors.New("the browser didn't allow RustWatch to connect")}
	}
	defer c.Close()
	log.Println("pairing: firefox bidi connected on", port)

	msgs := make(chan map[string]json.RawMessage, 64)
	readErr := make(chan error, 1)
	go func() {
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			var m map[string]json.RawMessage
			if json.Unmarshal(data, &m) == nil {
				msgs <- m
			}
		}
	}()
	id := 0
	call := func(method string, params any) (json.RawMessage, error) {
		id++
		myID := id
		b, _ := json.Marshal(map[string]any{"id": myID, "method": method, "params": params})
		if err := c.WriteMessage(ws.OpText, b); err != nil {
			return nil, err
		}
		timeout := time.After(15 * time.Second)
		for {
			select {
			case m := <-msgs:
				var gotID int
				if json.Unmarshal(m["id"], &gotID) == nil && gotID == myID {
					var typ string
					json.Unmarshal(m["type"], &typ)
					if typ == "error" {
						return nil, fmt.Errorf("%s: %s", method, m["message"])
					}
					return m["result"], nil
				}
			case err := <-readErr:
				return nil, err
			case <-timeout:
				return nil, fmt.Errorf("%s timed out", method)
			}
		}
	}
	if _, err := call("session.new", map[string]any{"capabilities": map[string]any{}}); err != nil {
		return "", "", launchError{err}
	}
	if _, err := call("session.subscribe", map[string]any{"events": []string{"script.message"}}); err != nil {
		return "", "", launchError{err}
	}
	if _, err := call("script.addPreloadScript", map[string]any{"functionDeclaration": bidiPreload,
		"arguments": []any{map[string]any{"type": "channel", "value": map[string]any{"channel": "rwToken"}}}}); err != nil {
		return "", "", launchError{err}
	}
	tree, err := call("browsingContext.getTree", map[string]any{"maxDepth": 0})
	if err != nil {
		return "", "", launchError{err}
	}
	var t struct {
		Contexts []struct {
			Context string `json:"context"`
		} `json:"contexts"`
	}
	json.Unmarshal(tree, &t)
	if len(t.Contexts) == 0 {
		return "", "", launchError{errors.New("couldn't find the browser tab")}
	}
	if _, err := call("browsingContext.navigate", map[string]any{"context": t.Contexts[0].Context, "url": RustPlusLoginURL, "wait": "none"}); err != nil {
		return "", "", launchError{err}
	}
	log.Println("pairing: login page opened (firefox)")
	for {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case err := <-readErr:
			return "", "", fmt.Errorf("Steam login window was closed before finishing (%v)", err)
		case m := <-msgs:
			var method string
			json.Unmarshal(m["method"], &method)
			if method != "script.message" {
				continue
			}
			var p struct {
				Channel string `json:"channel"`
				Data    struct {
					Value string `json:"value"`
				} `json:"data"`
			}
			json.Unmarshal(m["params"], &p)
			if p.Channel != "rwToken" {
				continue
			}
			if tok, sid, ok := parseAuth(p.Data.Value); ok {
				c.WriteMessage(ws.OpText, []byte(`{"id":9999,"method":"browser.close","params":{}}`))
				time.Sleep(300 * time.Millisecond)
				return tok, sid, nil
			}
		}
	}
}
