package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistryParsing(t *testing.T) {
	out := "\r\nHKEY_CURRENT_USER\\Software\\Microsoft\\Windows\\Shell\\Associations\\UrlAssociations\\https\\UserChoice\r\n    ProgId    REG_SZ    OperaGXStable\r\n\r\n"
	if v := regValue(out, "ProgId"); v != "OperaGXStable" {
		t.Fatalf("progid %q", v)
	}
	cmdOut := "\r\nHKEY_CLASSES_ROOT\\OperaGXStable\\shell\\open\\command\r\n    (Standard)    REG_SZ    \"C:\\Users\\Hawk\\AppData\\Local\\Programs\\Opera GX\\launcher.exe\" -noautoupdate -- \"%1\"\r\n"
	exe := exeFromCommand(regValue(cmdOut, ""))
	b, ok := browserFromPath(exe)
	if !ok || b.Name != "Opera GX" || b.Firefox || exe != `C:\Users\Hawk\AppData\Local\Programs\Opera GX\launcher.exe` {
		t.Fatalf("%q %+v", exe, b)
	}
	b, _ = browserFromPath(exeFromCommand(`"C:\Program Files\Mozilla Firefox\firefox.exe" -osint -url "%1"`))
	if !b.Firefox || b.Name != "Firefox" {
		t.Fatalf("%+v", b)
	}
	b, _ = browserFromPath(exeFromCommand(`C:\Program Files\Google\Chrome\Application\chrome.exe --single-argument %1`))
	if b.Name != "Google Chrome" {
		t.Fatalf("%+v", b)
	}
}

// The Rust+ login page calls ReactNativeWebView.postMessage synchronously on load, after
// bouncing through Steam. This checks the bridge is in place before page scripts run.
func TestChromiumLoginCapturesToken(t *testing.T) {
	chrome, err := exec.LookPath("google-chrome")
	if err != nil {
		t.Skip("no chrome")
	}
	wrapper := filepath.Join(t.TempDir(), "chrome.sh")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+chrome+" --headless=new --no-sandbox \"$@\"\n"), 0o755)

	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/steam", 302) })
	mux.HandleFunc("/steam", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body>Steam<script>setTimeout(()=>location.href="/done",300)</script></body></html>`)
	})
	mux.HandleFunc("/done", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><script>window.ReactNativeWebView.postMessage(JSON.stringify({SteamId:"76561198000000001",Token:"tok-123"}))</script></head><body>ok</body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old := RustPlusLoginURL
	RustPlusLoginURL = srv.URL + "/login"
	defer func() { RustPlusLoginURL = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	tok, sid, err := browserLogin(ctx, Browser{Name: "test", Path: wrapper}, filepath.Join(t.TempDir(), "prof"), 1, nil)
	if err != nil || tok != "tok-123" || sid != "76561198000000001" {
		t.Fatalf("tok=%q sid=%q err=%v", tok, sid, err)
	}
}
