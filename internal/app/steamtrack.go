package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"rustwatch/internal/a2s"
)

// Tracking by Steam profile. Many servers hide real names from their public player list
// (Rust's server.censorplayerlist, on by default), so matching by name can't work there.
// Steam's GetPlayerSummaries says which game server a public profile is playing on
// (gameserverip), which works whatever the server shows. Needs the free Steam Web API key.

type steamSummary struct {
	SteamID      string `json:"steamid"`
	PersonaName  string `json:"personaname"`
	PersonaState int    `json:"personastate"` // 0 offline (or invisible)
	Visibility   int    `json:"communityvisibilitystate"`
	GameID       string `json:"gameid"`
	GameServerIP string `json:"gameserverip"`
	ProfileURL   string `json:"profileurl"`
}

const rustAppID = "252490"

var (
	steamID64Re   = regexp.MustCompile(`\b(7656119\d{10})\b`)
	steamVanityRe = regexp.MustCompile(`steamcommunity\.com/id/([^/?#\s]+)`)
)

// looksLikeSteam reports whether the text is a Steam profile link or a SteamID64.
func looksLikeSteam(s string) bool {
	s = strings.TrimSpace(s)
	return steamID64Re.MatchString(s) || steamVanityRe.MatchString(s)
}

func steamGet(ctx context.Context, path string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", steamAPIBase+path, nil)
	resp, err := statsHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach Steam (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return errors.New("Steam rejected the API key — check it in Settings")
	}
	if resp.StatusCode == 429 {
		return errors.New("Steam rate limit hit, retrying shortly")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("Steam HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// resolveSteamID turns a SteamID64, /profiles/ link or /id/ custom link into a SteamID64.
func resolveSteamID(ctx context.Context, key, in string) (string, error) {
	in = strings.TrimSpace(in)
	if m := steamID64Re.FindStringSubmatch(in); m != nil {
		return m[1], nil
	}
	m := steamVanityRe.FindStringSubmatch(in)
	if m == nil {
		return "", errors.New("that isn't a Steam profile link — copy it from the player's Steam profile (steamcommunity.com/id/… or /profiles/…)")
	}
	if key == "" {
		return "", errors.New("add a free Steam Web API key in Settings → Steam first")
	}
	var out struct {
		Response struct {
			Success int    `json:"success"`
			SteamID string `json:"steamid"`
		} `json:"response"`
	}
	if err := steamGet(ctx, "/ISteamUser/ResolveVanityURL/v1/?key="+url.QueryEscape(key)+"&vanityurl="+url.QueryEscape(m[1]), &out); err != nil {
		return "", err
	}
	if out.Response.Success != 1 || out.Response.SteamID == "" {
		return "", fmt.Errorf("no Steam profile called %q", m[1])
	}
	return out.Response.SteamID, nil
}

// fetchSummaries looks up to 100 profiles per request.
func fetchSummaries(ctx context.Context, key string, ids []string) (map[string]steamSummary, error) {
	res := map[string]steamSummary{}
	for i := 0; i < len(ids); i += 100 {
		batch := ids[i:min(i+100, len(ids))]
		var out struct {
			Response struct {
				Players []steamSummary `json:"players"`
			} `json:"response"`
		}
		if err := steamGet(ctx, "/ISteamUser/GetPlayerSummaries/v2/?key="+url.QueryEscape(key)+"&steamids="+strings.Join(batch, ","), &out); err != nil {
			return nil, err
		}
		for _, p := range out.Response.Players {
			res[p.SteamID] = p
		}
	}
	return res, nil
}

// serverAddrs is what the tracker knows about where the active server lives.
type serverAddrs struct {
	ips   map[string]bool
	ports map[int]bool
	name  string
}

func (t *Tracker) addrsLocked(sc *ServerConfig, name string) serverAddrs {
	sa := serverAddrs{ips: map[string]bool{}, ports: map[int]bool{}, name: name}
	addHost := func(h string) {
		if h == "" {
			return
		}
		if ip := net.ParseIP(h); ip != nil {
			sa.ips[ip.String()] = true
		} else {
			sa.ips[h] = true // resolved later, outside the lock
		}
	}
	addHost(sc.IP)
	if sc.Port > 67 {
		sa.ports[sc.Port-67] = true // Rust+ app port defaults to game port + 67
	}
	for _, s := range []string{sc.QueryAddr, sc.QueryHint, t.queryAddr} {
		h, p, err := net.SplitHostPort(strings.TrimSpace(s))
		if err != nil {
			continue
		}
		addHost(h)
		if n, _ := strconv.Atoi(p); n > 0 {
			sa.ports[n] = true // the game port itself
			sa.ports[n-1] = true
			sa.ports[n-2] = true // query port is often game port + 1 or 2
		}
	}
	for p := range t.herePorts {
		sa.ports[p] = true
	}
	return sa
}

// resolve turns host names (from a typed address) into IPs.
func (sa serverAddrs) resolve(ctx context.Context) {
	for h := range sa.ips {
		if net.ParseIP(h) != nil {
			continue
		}
		if ips, err := net.DefaultResolver.LookupHost(ctx, h); err == nil {
			for _, ip := range ips {
				sa.ips[net.ParseIP(ip).String()] = true
			}
		}
	}
}

// where says whether a Steam game-server address is this server. Same IP and a known port
// is a match; same IP on another port is checked once by asking that address for its name.
func (t *Tracker) where(sa serverAddrs, gs string) (here bool, verifyPort int) {
	h, p, err := net.SplitHostPort(gs)
	if err != nil {
		return false, 0
	}
	ip := net.ParseIP(h)
	if ip == nil || ip.IsUnspecified() || !sa.ips[ip.String()] {
		return false, 0
	}
	n, _ := strconv.Atoi(p)
	if sa.ports[n] {
		return true, 0
	}
	return false, n
}

// steamPresence is the per-player Steam status shown in the tracked list.
type steamPresence struct {
	Here   bool
	Text   string
	Name   string
	Avatar string
}

// steamPoll looks up every Steam-linked tracked player. Called without a.mu held.
func (t *Tracker) steamPoll(ctx context.Context, sc *ServerConfig, key string, ids []string, sa serverAddrs) (map[string]steamPresence, error) {
	sum, err := fetchSummaries(ctx, key, ids)
	if err != nil {
		return nil, err
	}
	sa.resolve(ctx)
	out := map[string]steamPresence{}
	for _, id := range ids {
		s, ok := sum[id]
		if !ok {
			out[id] = steamPresence{Text: "profile not found"}
			continue
		}
		pr := steamPresence{Name: s.PersonaName}
		switch {
		case s.Visibility != 3:
			pr.Text = "profile is private, so RustWatch can't see what they're playing"
		case s.GameID == rustAppID && s.GameServerIP != "" && s.GameServerIP != "0.0.0.0:0":
			here, verify := t.where(sa, s.GameServerIP)
			if !here && verify > 0 {
				here = t.verifyHere(sa, s.GameServerIP, verify)
			}
			if here {
				pr.Here, pr.Text = true, "in Rust on this server"
			} else {
				pr.Text = "in Rust on another server (" + s.GameServerIP + ")"
			}
		case s.GameID == rustAppID:
			pr.Text = "in Rust, in the menus or loading"
		case s.GameID != "":
			pr.Text = "playing another game"
		case s.PersonaState == 0:
			pr.Text = "offline (or set to invisible)"
		default:
			pr.Text = "online, not in Rust"
		}
		out[id] = pr
	}
	return out, nil
}

// verifyHere asks a same-IP, unknown-port address for its server name and remembers the answer.
func (t *Tracker) verifyHere(sa serverAddrs, addr string, port int) bool {
	t.a.mu.Lock()
	known, done := t.portChecked[port]
	t.a.mu.Unlock()
	if done {
		return known
	}
	here := false
	if in, err := a2s.QueryInfo(addr, 3*time.Second); err == nil && sa.name != "" &&
		strings.EqualFold(strings.TrimSpace(in.Name), strings.TrimSpace(sa.name)) {
		here = true
	}
	t.a.mu.Lock()
	t.portChecked[port] = here
	if here {
		t.herePorts[port] = true
	}
	t.a.mu.Unlock()
	return here
}

// mergeSteamLocked adds Steam-linked tracked players who are on this server to the online
// list under "s:<steamid>", replacing a name entry for the same person if the server shows one.
func (t *Tracker) mergeSteamLocked(sc *ServerConfig, cur map[string]seenPlayer, pres map[string]steamPresence) {
	for i := range sc.Tracked {
		tp := &sc.Tracked[i]
		if tp.SteamID == "" {
			continue
		}
		pr, ok := pres[tp.SteamID]
		if !ok || !pr.Here {
			continue
		}
		if _, ok := cur["s:"+tp.SteamID]; ok {
			continue // already in the Steam client's list
		}
		sp := seenPlayer{Name: tp.Name}
		for _, n := range []string{pr.Name, tp.Name} {
			if n == "" {
				continue
			}
			found := false
			for k, p := range cur { // name key (direct) or BattleMetrics id
				if !strings.HasPrefix(k, "s:") && strings.EqualFold(p.Name, n) {
					sp.Dur, found = p.Dur, true
					delete(cur, k)
					break
				}
			}
			if found {
				break
			}
		}
		cur["s:"+tp.SteamID] = sp
	}
}
