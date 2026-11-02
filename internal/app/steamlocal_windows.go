//go:build windows

package app

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"rustwatch/internal/a2s"
)

// winSteam reads the Steam client's player list for the server you're in, using the
// steam_api64.dll that ships with Rust. It only attaches to Steam while RustClient.exe is
// running (so it never adds Rust playtime or shows you in-game on its own) and detaches
// within a few seconds of Rust closing. It never touches the Rust process itself.
type winSteam struct {
	reqs chan winReq
}

type winReq struct {
	hint  uint64
	close bool
	out   chan localSnap
}

func newLocalSteam() localSteamReader {
	w := &winSteam{reqs: make(chan winReq)}
	go w.loop()
	return w
}

func (w *winSteam) Snapshot(hint uint64) localSnap {
	out := make(chan localSnap, 1)
	w.reqs <- winReq{hint: hint, out: out}
	return <-out
}

func (w *winSteam) Close() {
	out := make(chan localSnap, 1)
	w.reqs <- winReq{close: true, out: out}
	<-out
}

// steamAPI is a loaded, initialised steam_api64.dll.
type steamAPI struct {
	dll     *syscall.DLL
	procs   map[string]*syscall.Proc
	friends uintptr
	user    uintptr
	me      uint64
}

func (w *winSteam) loop() {
	runtime.LockOSThread() // all Steam calls from one thread
	var api *steamAPI
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	shutdown := func() {
		if api != nil {
			api.shutdown()
			api = nil
			log.Println("steam client: detached (Rust closed)")
		}
	}
	for {
		select {
		case <-tick.C:
			if api != nil && !rustRunning() {
				shutdown()
			}
		case r := <-w.reqs:
			if r.close {
				shutdown()
				r.out <- localSnap{}
				return
			}
			if !rustRunning() {
				shutdown()
				r.out <- localSnap{State: "no-rust", Note: "Rust isn't running on this PC"}
				continue
			}
			if api == nil {
				var err error
				api, err = openSteamAPI()
				if err != nil {
					log.Println("steam client:", err)
					r.out <- localSnap{State: "unavailable", Note: err.Error()}
					continue
				}
				log.Println("steam client: attached")
			}
			r.out <- api.snapshot(r.hint)
		}
	}
}

func rustRunning() bool {
	h, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var pe syscall.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = syscall.Process32First(h, &pe); err == nil; err = syscall.Process32Next(h, &pe) {
		if strings.EqualFold(syscall.UTF16ToString(pe.ExeFile[:]), "RustClient.exe") {
			return true
		}
	}
	return false
}

var vdfPathRe = regexp.MustCompile(`"path"\s+"([^"]+)"`)

// findRustSteamAPI finds steam_api64.dll in the Rust install, via Steam's library folders.
func findRustSteamAPI() (string, error) {
	steam := regString(syscall.HKEY_CURRENT_USER, `Software\Valve\Steam`, "SteamPath")
	if steam == "" {
		return "", fmt.Errorf("couldn't find Steam on this PC")
	}
	steam = filepath.FromSlash(steam)
	libs := []string{steam}
	if b, err := os.ReadFile(filepath.Join(steam, "steamapps", "libraryfolders.vdf")); err == nil {
		for _, m := range vdfPathRe.FindAllStringSubmatch(string(b), -1) {
			libs = append(libs, strings.ReplaceAll(m[1], `\\`, `\`))
		}
	}
	for _, lib := range libs {
		root := filepath.Join(lib, "steamapps", "common", "Rust")
		cands := []string{
			filepath.Join(root, "RustClient_Data", "Plugins", "x86_64", "steam_api64.dll"),
			filepath.Join(root, "RustClient_Data", "Plugins", "steam_api64.dll"),
			filepath.Join(root, "steam_api64.dll"),
		}
		for _, c := range cands {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
		found := ""
		filepath.WalkDir(filepath.Join(root, "RustClient_Data"), func(p string, d os.DirEntry, err error) error {
			if err == nil && found == "" && !d.IsDir() && strings.EqualFold(d.Name(), "steam_api64.dll") {
				found = p
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
	}
	return "", fmt.Errorf("couldn't find Rust's steam_api64.dll in your Steam libraries")
}

func openSteamAPI() (*steamAPI, error) {
	path, err := findRustSteamAPI()
	if err != nil {
		return nil, err
	}
	os.Setenv("SteamAppId", rustAppID)
	os.Setenv("SteamGameId", rustAppID)
	dll, err := syscall.LoadDLL(path)
	if err != nil {
		return nil, fmt.Errorf("couldn't load %s: %v", path, err)
	}
	s := &steamAPI{dll: dll, procs: map[string]*syscall.Proc{}}
	fail := func(e error) (*steamAPI, error) { dll.Release(); return nil, e }
	if p, err := dll.FindProc("SteamAPI_InitFlat"); err == nil {
		var msg [1024]byte
		if r, _, _ := p.Call(uintptr(unsafe.Pointer(&msg[0]))); r != 0 {
			return fail(fmt.Errorf("Steam refused the connection (code %d: %s)", r, cstring(msg[:])))
		}
	} else if p, err := firstProc(dll, "SteamAPI_InitSafe", "SteamAPI_Init"); err == nil {
		if r, _, _ := p.Call(); r&0xff == 0 {
			return fail(fmt.Errorf("Steam refused the connection — is Steam running?"))
		}
	} else {
		return fail(fmt.Errorf("Rust's steam_api64.dll has no init function RustWatch knows"))
	}
	if p, err := firstProc(dll, "SteamAPI_SteamFriends_v018", "SteamAPI_SteamFriends_v017"); err == nil {
		s.friends, _, _ = p.Call()
	}
	if p, err := firstProc(dll, "SteamAPI_SteamUser_v023", "SteamAPI_SteamUser_v022", "SteamAPI_SteamUser_v021"); err == nil {
		s.user, _, _ = p.Call()
	}
	if s.friends == 0 || s.user == 0 {
		s.shutdown()
		return nil, fmt.Errorf("this version of Rust's steam_api64.dll isn't supported (no friends interface)")
	}
	s.me = uint64(s.call("SteamAPI_ISteamUser_GetSteamID", s.user))
	return s, nil
}

func firstProc(dll *syscall.DLL, names ...string) (*syscall.Proc, error) {
	var last error
	for _, n := range names {
		p, err := dll.FindProc(n)
		if err == nil {
			return p, nil
		}
		last = err
	}
	return nil, last
}

func (s *steamAPI) call(name string, args ...uintptr) uintptr {
	p := s.procs[name]
	if p == nil {
		var err error
		if p, err = s.dll.FindProc(name); err != nil {
			return 0
		}
		s.procs[name] = p
	}
	r, _, _ := p.Call(args...)
	return r
}

func (s *steamAPI) shutdown() {
	s.call("SteamAPI_Shutdown")
	s.dll.Release()
}

// friendGameInfo mirrors FriendGameInfo_t.
type friendGameInfo struct {
	GameID    uint64
	IP        uint32
	GamePort  uint16
	QueryPort uint16
	Lobby     uint64
}

func (s *steamAPI) snapshot(hint uint64) localSnap {
	s.call("SteamAPI_RunCallbacks")
	snap := localSnap{Recent: s.recentRust()}
	server := hint
	// which server am I on? Steam knows the address; its query reply gives the server's Steam ID
	var gi friendGameInfo
	if s.call("SteamAPI_ISteamFriends_GetFriendGamePlayed", s.friends, uintptr(s.me), uintptr(unsafe.Pointer(&gi)))&0xff != 0 && gi.IP != 0 {
		ip := net.IPv4(byte(gi.IP>>24), byte(gi.IP>>16), byte(gi.IP>>8), byte(gi.IP)).String()
		snap.ServerAddr = net.JoinHostPort(ip, strconv.Itoa(int(gi.GamePort)))
		for _, port := range []uint16{gi.QueryPort, gi.GamePort, gi.GamePort + 1} {
			if port == 0 || port == 0xFFFF {
				continue
			}
			if in, err := a2s.QueryInfo(net.JoinHostPort(ip, strconv.Itoa(int(port))), 3*time.Second); err == nil {
				snap.ServerName = in.Name
				if in.SteamID != 0 {
					server = in.SteamID
				}
				break
			}
		}
	}
	if server == 0 {
		snap.State, snap.Note = "not-in-server", "Rust is running but Steam doesn't show you on a server yet"
		return snap
	}
	n := int(int32(s.call("SteamAPI_ISteamFriends_GetFriendCountFromSource", s.friends, uintptr(server))))
	for i := 0; i < n && i < 1000; i++ {
		id := uint64(s.call("SteamAPI_ISteamFriends_GetFriendFromSourceByIndex", s.friends, uintptr(server), uintptr(i)))
		if id == 0 || id == s.me {
			continue
		}
		snap.Players = append(snap.Players, localPlayer{ID: strconv.FormatUint(id, 10), Name: s.name(id)})
	}
	if len(snap.Players) == 0 {
		snap.State = "not-in-server"
		snap.Note = "Steam has no full player list for this server"
		if snap.ServerAddr == "" {
			snap.Note += " (Steam doesn't show you on a server)"
		}
		return snap
	}
	snap.State = "ok"
	return snap
}

// name returns a player's Steam name, asking Steam for it if it isn't cached yet.
func (s *steamAPI) name(id uint64) string {
	name := cstrAt(s.call("SteamAPI_ISteamFriends_GetFriendPersonaName", s.friends, uintptr(id)))
	if name == "" || name == "[unknown]" {
		s.call("SteamAPI_ISteamFriends_RequestUserInformation", s.friends, uintptr(id), 1)
		return ""
	}
	return name
}

// recentRust is Steam's "recently played with" list for Rust (the Steam overlay's Players
// tab): up to 100 people, each with when you last played with them.
func (s *steamAPI) recentRust() []localPlayer {
	var out []localPlayer
	c := int(int32(s.call("SteamAPI_ISteamFriends_GetCoplayFriendCount", s.friends)))
	for i := 0; i < c && i < 1000; i++ {
		id := uint64(s.call("SteamAPI_ISteamFriends_GetCoplayFriend", s.friends, uintptr(i)))
		if id == 0 || id == s.me || s.call("SteamAPI_ISteamFriends_GetFriendCoplayGame", s.friends, uintptr(id)) != 252490 {
			continue
		}
		at := int64(uint32(s.call("SteamAPI_ISteamFriends_GetFriendCoplayTime", s.friends, uintptr(id))))
		out = append(out, localPlayer{ID: strconv.FormatUint(id, 10), Name: s.name(id), At: at})
	}
	return out
}

func cstring(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// cstrAt reads a NUL-terminated UTF-8 string returned by the Steam API.
func cstrAt(p uintptr) string {
	if p == 0 {
		return ""
	}
	base := *(*unsafe.Pointer)(unsafe.Pointer(&p))
	var b []byte
	for i := 0; i < 256; i++ {
		c := *(*byte)(unsafe.Add(base, i))
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}
