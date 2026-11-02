package app

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"rustwatch/internal/a2s"
)

// Tracker follows who is online on the active server and alerts when tagged ("tracked")
// players join or leave. By default it asks the Rust server directly (Steam A2S query, free);
// with a BattleMetrics API key it uses BattleMetrics instead (stable player ids across name changes).
type Tracker struct {
	a       *App
	pokeCh  chan struct{}
	http    *http.Client
	baseURL string

	// fields below are guarded by a.mu
	serverID   string
	serverName string
	status     string
	lastPoll   time.Time
	online     map[string]*OnlinePlayer
	activity   []Activity
	first      bool
	bmPlayers  int
	bmMax      int
	bmQueue    int
	source     string // "direct" or "battlemetrics"
	censored   bool   // server replaces real names with random ones (server.censorplayerlist)
	shown      int    // names in the server's reply
	total      int    // players the server says are online
	queryAddr  string
	key        string

	steamOn     bool                     // a Steam key is set: Steam-linked players are followed by Steam presence
	steam       map[string]steamPresence // steamId -> latest presence
	steamErr    string
	herePorts   map[int]bool // extra game ports confirmed to be this server
	portChecked map[int]bool

	local         localSteamReader  // the Steam client's list for the server you're in (Windows)
	localMode     bool              // the online list currently comes from the Steam client
	localState    string            // why the Steam client list is or isn't used
	names         map[string]string // steamId -> Steam name cache
	serverSteamID uint64            // the active server's Steam ID, from its query reply
}

type OnlinePlayer struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Since time.Time `json:"-"`
	Known bool      `json:"-"` // Since is accurate (seen joining)
}

type Activity struct {
	Time   int64  `json:"time"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Joined bool   `json:"joined"`
	Tag    string `json:"tag"`
}

func newTracker(a *App) *Tracker {
	return &Tracker{a: a, pokeCh: make(chan struct{}, 1), http: &http.Client{Timeout: 25 * time.Second},
		baseURL: "https://api.battlemetrics.com", online: map[string]*OnlinePlayer{}, first: true, status: "idle",
		steam: map[string]steamPresence{}, herePorts: map[int]bool{}, portChecked: map[int]bool{},
		names: map[string]string{}, local: newLocalSteam()}
}

func (t *Tracker) poke() {
	select {
	case t.pokeCh <- struct{}{}:
	default:
	}
}

func (t *Tracker) run() {
	for {
		t.a.mu.Lock()
		interval := time.Duration(t.a.cfg.Settings.BMPollSeconds) * time.Second
		t.a.mu.Unlock()
		t.poll()
		select {
		case <-t.pokeCh:
			time.Sleep(2 * time.Second) // let server info load / debounce
		case <-time.After(interval):
		}
	}
}

func (t *Tracker) get(ctx context.Context, path string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", t.baseURL+path, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "RustWatch/"+Version)
	t.a.mu.Lock()
	tok := t.a.cfg.Settings.BMToken
	t.a.mu.Unlock()
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == 429 {
		return errors.New("BattleMetrics rate limit hit, slowing down")
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return errors.New("BattleMetrics rejected your API key — it needs an active BattleMetrics subscription. Clear the key in Settings to use the free direct server query instead")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("BattleMetrics HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

type BMServer struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IP         string `json:"ip"`
	Port       int    `json:"port"`
	Players    int    `json:"players"`
	MaxPlayers int    `json:"maxPlayers"`
	Status     string `json:"status"`
	Country    string `json:"country"`
}

type bmServerData struct {
	ID         string `json:"id"`
	Attributes struct {
		Name       string `json:"name"`
		IP         string `json:"ip"`
		Port       int    `json:"port"`
		Players    int    `json:"players"`
		MaxPlayers int    `json:"maxPlayers"`
		Status     string `json:"status"`
		Country    string `json:"country"`
		Details    struct {
			Queue int `json:"rust_queued_players"`
		} `json:"details"`
	} `json:"attributes"`
}

func (d bmServerData) server() BMServer {
	x := d.Attributes
	return BMServer{ID: d.ID, Name: x.Name, IP: x.IP, Port: x.Port, Players: x.Players, MaxPlayers: x.MaxPlayers, Status: x.Status, Country: x.Country}
}

// SearchServers searches BattleMetrics for Rust servers by name or IP.
func (t *Tracker) SearchServers(ctx context.Context, q string) ([]BMServer, error) {
	var out struct {
		Data []bmServerData `json:"data"`
	}
	path := "/servers?filter[game]=rust&page[size]=15&filter[search]=" + url.QueryEscape(q)
	if err := t.get(ctx, path, &out); err != nil {
		return nil, err
	}
	var res []BMServer
	for _, d := range out.Data {
		res = append(res, d.server())
	}
	return res, nil
}

var bmIDRe = regexp.MustCompile(`(\d{4,})`)

// ParseBMID accepts a raw id or a battlemetrics.com URL.
func ParseBMID(s string) string {
	m := bmIDRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

func (t *Tracker) resolveServer(ctx context.Context, name, ip string) (string, error) {
	if name == "" {
		return "", errors.New("waiting for server info")
	}
	res, err := t.SearchServers(ctx, name)
	if err != nil {
		return "", err
	}
	for _, s := range res {
		if s.Name == name && (ip == "" || s.IP == ip) {
			return s.ID, nil
		}
	}
	for _, s := range res {
		if s.Name == name {
			return s.ID, nil
		}
	}
	if ip != "" {
		res2, err := t.SearchServers(ctx, ip)
		if err == nil && len(res2) == 1 {
			return res2[0].ID, nil
		}
	}
	return "", errors.New("couldn't find this server on BattleMetrics — link it manually in Players")
}

func (t *Tracker) setStatus(s string) {
	t.a.mu.Lock()
	t.status = s
	t.a.mu.Unlock()
	t.a.publish("update", nil)
}

type seenPlayer struct {
	Name string
	Dur  time.Duration // how long they've been connected (direct query only)
}

func (t *Tracker) poll() {
	a := t.a
	a.mu.Lock()
	sc := a.srv
	if sc == nil {
		t.status = "no server selected"
		a.mu.Unlock()
		return
	}
	useBM := a.cfg.Settings.BMToken != ""
	name, ip := sc.Name, sc.IP
	if a.info != nil {
		name = a.info.Name
	}
	source := "direct"
	key := "direct:" + sc.ID
	if useBM {
		source, key = "battlemetrics", "bm:"+sc.ID+":"+sc.BMServerID
	}
	if t.key != key {
		t.key, t.source, t.online, t.first, t.activity = key, source, map[string]*OnlinePlayer{}, true, nil
		t.serverID, t.serverName, t.queryAddr = sc.BMServerID, "", sc.QueryAddr
		t.status, t.lastPoll = "loading the player list…", time.Time{}
		t.censored, t.shown, t.total = false, 0, 0
		t.steam, t.steamErr, t.herePorts, t.portChecked = map[string]steamPresence{}, "", map[int]bool{}, map[int]bool{}
		t.localMode, t.localState, t.names, t.serverSteamID = false, "", map[string]string{}, 0
	}
	local, hint := t.local, t.serverSteamID
	steamKey := a.cfg.Settings.SteamAPIKey
	t.steamOn = steamKey != ""
	var steamIDs []string
	for _, tp := range sc.Tracked {
		if tp.SteamID != "" {
			steamIDs = append(steamIDs, tp.SteamID)
		}
	}
	sa := t.addrsLocked(sc, name)
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	var pres map[string]steamPresence
	var serr error
	steamDone := make(chan struct{})
	go func() {
		defer close(steamDone)
		if steamKey != "" && len(steamIDs) > 0 {
			pres, serr = t.steamPoll(ctx, sc, steamKey, steamIDs, sa)
		}
	}()
	var snap localSnap
	localDone := make(chan struct{})
	go func() {
		defer close(localDone)
		if local != nil {
			snap = local.Snapshot(hint)
		}
	}()
	var cur map[string]seenPlayer
	var err error
	if useBM {
		cur, err = t.fetchBM(ctx, sc, name, ip)
	} else {
		cur, err = t.fetchDirect(ctx, sc, name, ip)
	}
	<-steamDone
	<-localDone
	useLocal, localState := t.judgeLocal(snap, sa, local != nil)
	if useLocal {
		t.fillNames(ctx, steamKey, snap.Players)
	}
	if err != nil && pres == nil && !useLocal {
		a.mu.Lock()
		t.localState = localState
		a.mu.Unlock()
		t.setStatus(err.Error())
		return
	}
	a.mu.Lock()
	defer func() {
		a.mu.Unlock()
		a.publish("update", nil)
	}()
	if a.srv != sc || t.source != source {
		return
	}
	t.localState = localState
	if useLocal != t.localMode { // the list's keys change: start over quietly instead of mass joins/leaves
		t.localMode, t.online, t.first = useLocal, map[string]*OnlinePlayer{}, true
	}
	if useLocal {
		cur = t.localListLocked(sc, snap.Players, cur)
		t.shown = len(cur)
		err = nil
	} else if err != nil {
		// the player list failed but Steam answered: keep the list as it was, update Steam players
		cur = map[string]seenPlayer{}
		for k, p := range t.online {
			if !strings.HasPrefix(k, "s:") {
				cur[k] = seenPlayer{Name: p.Name}
			}
		}
	}
	t.steamErr = ""
	if serr != nil {
		t.steamErr = serr.Error()
		for k, p := range t.online { // Steam hiccup: leave Steam-linked players as they were
			if strings.HasPrefix(k, "s:") {
				cur[k] = seenPlayer{Name: p.Name}
			}
		}
	} else if pres != nil {
		for id, p := range pres {
			t.steam[id] = p
		}
		t.mergeSteamLocked(sc, cur, pres)
	}
	t.apply(sc, cur)
	if err != nil {
		t.status = err.Error()
	}
}

func (t *Tracker) fetchBM(ctx context.Context, sc *ServerConfig, name, ip string) (map[string]seenPlayer, error) {
	a := t.a
	a.mu.Lock()
	bmID := sc.BMServerID
	a.mu.Unlock()
	if bmID == "" {
		id, err := t.resolveServer(ctx, name, ip)
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		if a.srv == sc {
			sc.BMServerID = id
			t.key = "bm:" + sc.ID + ":" + id // same session, don't reset again on the next poll
			a.saveLocked()
		}
		a.mu.Unlock()
		bmID = id
	}
	var out struct {
		Data     bmServerData `json:"data"`
		Included []struct {
			Type       string `json:"type"`
			ID         string `json:"id"`
			Attributes struct {
				Name string `json:"name"`
			} `json:"attributes"`
		} `json:"included"`
	}
	if err := t.get(ctx, "/servers/"+bmID+"?include=player", &out); err != nil {
		return nil, err
	}
	cur := map[string]seenPlayer{}
	for _, inc := range out.Included {
		if inc.Type == "player" {
			cur[inc.ID] = seenPlayer{Name: cleanName(inc.Attributes.Name)}
		}
	}
	a.mu.Lock()
	t.serverID = bmID
	t.serverName = out.Data.Attributes.Name
	t.bmPlayers, t.bmMax, t.bmQueue = out.Data.Attributes.Players, out.Data.Attributes.MaxPlayers, out.Data.Attributes.Details.Queue
	a.mu.Unlock()
	return cur, nil
}

// directKey identifies a player by name in direct mode (the query protocol gives no ids).
func directKey(name string) string { return "n:" + strings.ToLower(strings.TrimSpace(name)) }

func (t *Tracker) fetchDirect(ctx context.Context, sc *ServerConfig, name, ip string) (map[string]seenPlayer, error) {
	a := t.a
	a.mu.Lock()
	addr, hint := sc.QueryAddr, sc.QueryHint
	appPort := sc.Port
	a.mu.Unlock()
	if addr == "" {
		var err error
		addr, err = findQueryAddr(ctx, ip, appPort, hint, name)
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		if a.srv == sc {
			sc.QueryAddr = addr
			a.saveLocked()
		}
		a.mu.Unlock()
	}
	info, err := a2s.QueryInfo(addr, 4*time.Second)
	if err != nil {
		return nil, fmt.Errorf("the server didn't answer the player query at %s (%v) — check the address in Players", addr, err)
	}
	queue := 0
	for _, kw := range strings.Split(info.Keywords, ",") {
		if strings.HasPrefix(kw, "qp") {
			queue, _ = strconv.Atoi(kw[2:])
		}
	}
	a.mu.Lock()
	t.queryAddr, t.serverName = addr, info.Name
	t.bmPlayers, t.bmMax, t.bmQueue = info.Players, info.MaxPlayers, queue
	a.mu.Unlock()
	list, err := a2s.QueryPlayerList(addr, 4*time.Second)
	players := list.Players
	if errors.Is(err, a2s.ErrNoPlayerList) {
		return nil, fmt.Errorf("this server answers server-browser queries but won't share its player list (%d online) — common on servers behind DDoS protection. Players tracked by Steam profile are still followed", info.Players)
	}
	if err != nil {
		return nil, fmt.Errorf("the server's player list didn't come through (%v) — retrying on the next refresh", err)
	}
	cur := map[string]seenPlayer{}
	for _, p := range players {
		n := cleanName(p.Name)
		if n == "" {
			continue
		}
		k := directKey(n)
		for i := 2; ; i++ {
			if _, dup := cur[k]; !dup {
				break
			}
			k = fmt.Sprintf("%s#%d", directKey(n), i)
		}
		cur[k] = seenPlayer{Name: n, Dur: p.Duration}
	}
	// what the server is willing to show: censored (random stand-in names) and/or a partial list
	named := 0
	for _, p := range players {
		if isRandomName(p.Name) {
			named++
		}
	}
	a.mu.Lock()
	t.censored = len(players) >= 3 && named*10 >= len(players)*8
	t.shown, t.total = len(cur), max(info.Players, list.Declared)
	if info.SteamID != 0 {
		t.serverSteamID = info.SteamID
	}
	a.mu.Unlock()
	if len(players) == 0 && info.Players > 0 {
		return nil, fmt.Errorf("this server hides its player list (%d players online), so names can't be matched here — players tracked by Steam profile are still followed", info.Players)
	}
	return cur, nil
}

// findQueryAddr probes the usual Rust query ports. Rust+ only tells us the app port, which by
// default is the game port + 67; the query port is usually the game port or one or two above it.
func findQueryAddr(ctx context.Context, ip string, appPort int, hint, wantName string) (string, error) {
	hostOf := func(s string) (string, int) {
		h, p, err := net.SplitHostPort(strings.TrimSpace(s))
		if err != nil {
			return "", 0
		}
		n, _ := strconv.Atoi(p)
		return h, n
	}
	var cands []string
	seen := map[string]bool{}
	add := func(h string, p int) {
		if h == "" || p <= 0 || p > 65535 {
			return
		}
		a := net.JoinHostPort(h, strconv.Itoa(p))
		if !seen[a] {
			seen[a] = true
			cands = append(cands, a)
		}
	}
	if hint != "" {
		h, p := hostOf(hint)
		if h == "" { // no port given
			h, p = strings.TrimSpace(hint), 28015
		}
		for _, d := range []int{0, 1, 2, 5, 10, -1} {
			add(h, p+d)
		}
	}
	game := appPort - 67
	for _, d := range []int{0, 1, 2, 5, 10} {
		add(ip, game+d)
	}
	for _, p := range []int{28015, 28016, 28017, appPort + 1, appPort - 1} {
		add(ip, p)
	}
	type res struct {
		addr string
		name string
	}
	ch := make(chan res, len(cands))
	for _, c := range cands {
		go func(c string) {
			in, err := a2s.QueryInfo(c, 2500*time.Millisecond)
			if err != nil {
				ch <- res{}
				return
			}
			ch <- res{c, in.Name}
		}(c)
	}
	var found []res
	for range cands {
		select {
		case r := <-ch:
			if r.addr != "" {
				found = append(found, r)
			}
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	for _, r := range found {
		if strings.EqualFold(strings.TrimSpace(r.name), strings.TrimSpace(wantName)) {
			return r.addr, nil
		}
	}
	if len(found) > 0 {
		return found[0].addr, nil
	}
	return "", errors.New("couldn't find this server's query port. In Rust press F1, type 'client.connect' history or check the server's connect address (IP:port), and enter it under Players → Server address")
}

// apply diffs the new player list. Caller holds a.mu.
func (t *Tracker) apply(sc *ServerConfig, cur map[string]seenPlayer) {
	a := t.a
	now := time.Now()
	t.lastPoll = now
	t.status = "ok"
	direct := t.source == "direct"
	tracked := map[string]*TrackedPlayer{}
	for i := range sc.Tracked {
		tp := &sc.Tracked[i]
		if tp.SteamID != "" && t.bySteam() {
			tracked["s:"+tp.SteamID] = tp
			continue
		}
		if direct {
			if tp.Name != "" && !strings.HasPrefix(tp.Name, "BM #") {
				tracked[directKey(tp.Name)] = tp
			}
			continue
		}
		if tp.BMID == "" {
			for id, p := range cur {
				if strings.EqualFold(p.Name, tp.Name) {
					tp.BMID = id
				}
			}
		}
		if tp.BMID != "" {
			tracked[tp.BMID] = tp
		}
	}
	for id, sp := range cur {
		p := t.online[id]
		if p == nil {
			since, known := now, !t.first
			if sp.Dur > 0 {
				since, known = now.Add(-sp.Dur), true
			}
			p = &OnlinePlayer{ID: id, Name: sp.Name, Since: since, Known: known}
			t.online[id] = p
			if !t.first {
				t.addActivity(id, sp.Name, true, tracked[id])
			}
			if tp := tracked[id]; tp != nil {
				tp.Name, tp.LastSeen = sp.Name, now.Unix()
				if len(tp.Sessions) == 0 || tp.Sessions[len(tp.Sessions)-1].End != 0 {
					tp.Sessions = append(tp.Sessions, Session{Start: since.Unix()})
					if len(tp.Sessions) > 200 {
						tp.Sessions = tp.Sessions[len(tp.Sessions)-200:]
					}
				}
				if !t.first && tp.Notify {
					a.alertLocked("tracked_online", tagged(tp)+" is online", fmt.Sprintf("%s joined the server", tagged(tp)))
				}
			}
		} else {
			p.Name = sp.Name
			if sp.Dur > 0 && sp.Dur < now.Sub(p.Since)-2*time.Minute {
				p.Since = now.Add(-sp.Dur) // reconnected between polls
			}
			if tp := tracked[id]; tp != nil {
				tp.LastSeen = now.Unix()
				tp.Name = sp.Name
			}
		}
	}
	for id, p := range t.online {
		if _, ok := cur[id]; ok {
			continue
		}
		delete(t.online, id)
		t.addActivity(id, p.Name, false, tracked[id])
		if tp := tracked[id]; tp != nil {
			tp.LastSeen = now.Unix()
			dur := ""
			if n := len(tp.Sessions); n > 0 && tp.Sessions[n-1].End == 0 {
				tp.Sessions[n-1].End = now.Unix()
				if p.Known {
					dur = " after " + fmtDur(now.Sub(time.Unix(tp.Sessions[n-1].Start, 0)))
				}
			}
			if tp.Notify {
				a.alertLocked("tracked_offline", tagged(tp)+" went offline", fmt.Sprintf("%s left the server%s", tagged(tp), dur))
			}
		}
	}
	if t.first {
		for k, tp := range tracked {
			if _, on := cur[k]; !on {
				if n := len(tp.Sessions); n > 0 && tp.Sessions[n-1].End == 0 {
					tp.Sessions[n-1].End = now.Unix()
				}
			}
		}
	}
	t.first = false
	a.saveLocked()
}

func cleanName(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\u200b' || r == '\u200c' || r == '\u200d' || r == '\ufeff' {
			return -1
		}
		return r
	}, s))
}

func tagged(tp *TrackedPlayer) string {
	if tp.Tag != "" {
		return "[" + tp.Tag + "] " + tp.Name
	}
	return tp.Name
}

func (t *Tracker) addActivity(id, name string, joined bool, tp *TrackedPlayer) {
	act := Activity{Time: time.Now().Unix(), ID: id, Name: name, Joined: joined}
	if tp != nil {
		act.Tag = tp.Tag
		if act.Tag == "" {
			act.Tag = "tracked"
		}
	}
	t.activity = append(t.activity, act)
	if len(t.activity) > 400 {
		t.activity = t.activity[len(t.activity)-400:]
	}
}

func (t *Tracker) onlineCount() int { return len(t.online) }

// keyFor is the key a tracked player has in t.online for the current source.
func (t *Tracker) keyFor(tp *TrackedPlayer) string {
	if tp.SteamID != "" && t.bySteam() {
		return "s:" + tp.SteamID
	}
	if t.source == "direct" {
		return directKey(tp.Name)
	}
	return tp.BMID
}

func (t *Tracker) chatSummaryLocked() string {
	sc := t.a.srv
	if sc == nil || len(sc.Tracked) == 0 {
		return "No tracked players. Tag players in the RustWatch app (Players tab)."
	}
	var on, off []string
	for i := range sc.Tracked {
		tp := &sc.Tracked[i]
		if k := t.keyFor(tp); k != "" && t.online[k] != nil {
			on = append(on, tagged(tp))
		} else {
			s := tagged(tp)
			if tp.LastSeen > 0 {
				s += " (" + fmtDur(time.Since(time.Unix(tp.LastSeen, 0))) + ")"
			}
			off = append(off, s)
		}
	}
	return "Tracked online: " + listOr(on) + " | Offline: " + listOr(off)
}

type OnlineView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	OnlineFor string `json:"onlineFor"`
	Known     bool   `json:"known"`
	Since     int64  `json:"since"`
	Tracked   bool   `json:"tracked"`
	Tag       string `json:"tag"`
	Color     string `json:"color"`
}

type TrackedView struct {
	TrackedPlayer
	Online      bool   `json:"online"`
	OnlineFor   string `json:"onlineFor"`
	LastSeenAgo string `json:"lastSeenAgo"`
	Playtime24  string `json:"playtime24h"`
	Playtime7d  string `json:"playtime7d"`
	Presence    string `json:"presence"`  // Steam status for Steam-linked players
	SteamName   string `json:"steamName"` // current Steam name
}

type TrackerView struct {
	ServerID   string        `json:"serverId"`
	Source     string        `json:"source"`
	Censored   bool          `json:"censored"`
	SteamOn    bool          `json:"steamOn"`
	LocalMode  bool          `json:"localMode"`
	LocalState string        `json:"localState"`
	SteamErr   string        `json:"steamErr"`
	Shown      int           `json:"shown"`
	Total      int           `json:"total"`
	QueryAddr  string        `json:"queryAddr"`
	ServerName string        `json:"serverName"`
	Status     string        `json:"status"`
	LastPoll   int64         `json:"lastPoll"`
	Players    int           `json:"players"`
	MaxPlayers int           `json:"maxPlayers"`
	Queue      int           `json:"queue"`
	Online     []OnlineView  `json:"online"`
	Tracked    []TrackedView `json:"tracked"`
	Activity   []Activity    `json:"activity"`
}

func playtime(ss []Session, since time.Time, now time.Time) time.Duration {
	var d time.Duration
	for _, s := range ss {
		st, en := time.Unix(s.Start, 0), now
		if s.End != 0 {
			en = time.Unix(s.End, 0)
		}
		if en.Before(since) {
			continue
		}
		if st.Before(since) {
			st = since
		}
		d += en.Sub(st)
	}
	return d
}

func (t *Tracker) viewLocked() TrackerView {
	now := time.Now()
	v := TrackerView{LocalMode: t.localMode, LocalState: t.localState, SteamOn: t.steamOn, SteamErr: t.steamErr, Censored: t.censored, Shown: t.shown, Total: t.total, Source: t.source, QueryAddr: t.queryAddr, ServerID: t.serverID, ServerName: t.serverName, Status: t.status, Players: t.bmPlayers, MaxPlayers: t.bmMax, Queue: t.bmQueue}
	if !t.lastPoll.IsZero() {
		v.LastPoll = t.lastPoll.Unix()
	}
	tmap := map[string]*TrackedPlayer{}
	if sc := t.a.srv; sc != nil {
		for i := range sc.Tracked {
			tp := &sc.Tracked[i]
			if k := t.keyFor(tp); k != "" {
				tmap[k] = tp
			}
			tv := TrackedView{TrackedPlayer: *tp}
			if tp.SteamID != "" {
				switch pr, ok := t.steam[tp.SteamID]; {
				case !t.steamOn:
					tv.Presence = "Add a free Steam Web API key in Settings to follow this Steam profile"
				case ok:
					tv.Presence, tv.SteamName = pr.Text, pr.Name
				default:
					tv.Presence = "Checking Steam…"
				}
			}
			if p, ok := t.online[t.keyFor(tp)]; ok && t.keyFor(tp) != "" {
				tv.Online = true
				tv.OnlineFor = fmtDur(now.Sub(p.Since))
				if !p.Known {
					tv.OnlineFor = "≥ " + tv.OnlineFor
				}
			}
			if tp.LastSeen > 0 {
				tv.LastSeenAgo = fmtDur(now.Sub(time.Unix(tp.LastSeen, 0)))
			}
			tv.Playtime24 = fmtDur(playtime(tp.Sessions, now.Add(-24*time.Hour), now))
			tv.Playtime7d = fmtDur(playtime(tp.Sessions, now.Add(-7*24*time.Hour), now))
			if len(tv.Sessions) > 50 {
				tv.Sessions = tv.Sessions[len(tv.Sessions)-50:]
			}
			v.Tracked = append(v.Tracked, tv)
		}
	}
	for _, p := range t.online {
		ov := OnlineView{ID: p.ID, Name: p.Name, Known: p.Known, Since: p.Since.Unix(), OnlineFor: fmtDur(now.Sub(p.Since))}
		if !p.Known {
			ov.OnlineFor = "≥ " + ov.OnlineFor
		}
		if tp := tmap[p.ID]; tp != nil {
			ov.Tracked, ov.Tag, ov.Color = true, tp.Tag, tp.Color
		}
		v.Online = append(v.Online, ov)
	}
	sort.Slice(v.Online, func(i, j int) bool {
		if v.Online[i].Tracked != v.Online[j].Tracked {
			return v.Online[i].Tracked
		}
		return strings.ToLower(v.Online[i].Name) < strings.ToLower(v.Online[j].Name)
	})
	sort.SliceStable(v.Tracked, func(i, j int) bool {
		if v.Tracked[i].Online != v.Tracked[j].Online {
			return v.Tracked[i].Online
		}
		return strings.ToLower(v.Tracked[i].Name) < strings.ToLower(v.Tracked[j].Name)
	})
	n := len(t.activity)
	for i := n - 1; i >= 0 && i >= n-150; i-- {
		v.Activity = append(v.Activity, t.activity[i])
	}
	return v
}

// Track adds or updates a tracked player on the active server. tp.SteamID may be a Steam
// profile link; it's resolved to a SteamID64 here.
func (a *App) Track(tp TrackedPlayer) error {
	a.mu.Lock()
	key := a.cfg.Settings.SteamAPIKey
	a.mu.Unlock()
	if strings.HasPrefix(tp.BMID, "s:") { // "Track" pressed on a player from the Steam client's list
		tp.SteamID, tp.BMID = tp.BMID[2:], ""
	}
	if raw := strings.TrimSpace(tp.SteamID); raw != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		id, err := resolveSteamID(ctx, key, raw)
		if err == nil && strings.TrimSpace(tp.Name) == "" && key != "" {
			if sum, e := fetchSummaries(ctx, key, []string{id}); e == nil {
				tp.Name = cleanName(sum[id].PersonaName)
			}
		}
		cancel()
		if err != nil {
			return err
		}
		tp.SteamID = id
		if strings.TrimSpace(tp.Name) == "" {
			tp.Name = "Steam " + id
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.srv == nil {
		return errors.New("connect to a server first")
	}
	if tp.BMID != "" && !strings.HasPrefix(tp.BMID, "n:") && !strings.HasPrefix(tp.BMID, "s:") {
		tp.BMID = ParseBMID(tp.BMID)
	} else {
		tp.BMID = ""
	}
	if tp.BMID == "" && tp.SteamID == "" && strings.TrimSpace(tp.Name) == "" {
		return errors.New("enter a player name, Steam profile link or BattleMetrics link")
	}
	for i := range a.srv.Tracked {
		ex := &a.srv.Tracked[i]
		same := false
		switch {
		case tp.SteamID != "" && ex.SteamID == tp.SteamID:
			same = true
		case tp.BMID != "" && ex.BMID == tp.BMID:
			same = true
		case tp.BMID == "" && tp.SteamID == "" && strings.EqualFold(ex.Name, tp.Name):
			same = true
		case tp.SteamID != "" && ex.SteamID == "" && tp.BMID == "" && strings.EqualFold(ex.Name, tp.Name):
			same = true // linking a Steam profile to someone already tracked by name
		}
		if same {
			ex.Tag, ex.Color, ex.Notes, ex.Notify = tp.Tag, tp.Color, tp.Notes, tp.Notify
			if tp.Name != "" {
				ex.Name = tp.Name
			}
			if tp.SteamID != "" {
				ex.SteamID = tp.SteamID
			}
			a.saveLocked()
			go a.tracker.poke()
			return nil
		}
	}
	if tp.Name == "" {
		tp.Name = "BM #" + tp.BMID
		if p, ok := a.tracker.online[a.tracker.keyFor(&tp)]; ok {
			tp.Name = p.Name
		}
	}
	if p, ok := a.tracker.online[a.tracker.keyFor(&tp)]; ok && a.tracker.keyFor(&tp) != "" {
		tp.LastSeen = time.Now().Unix()
		tp.Sessions = append(tp.Sessions, Session{Start: p.Since.Unix()})
	}
	a.srv.Tracked = append(a.srv.Tracked, tp)
	a.saveLocked()
	go a.tracker.poke()
	return nil
}

// Untrack removes a tracked player, identified by the first of steamID, bmID or name given.
func (a *App) Untrack(bmID, name string, steamID ...string) {
	sid := ""
	if len(steamID) > 0 {
		sid = steamID[0]
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.srv == nil {
		return
	}
	var out []TrackedPlayer
	for _, tp := range a.srv.Tracked {
		switch {
		case sid != "" && tp.SteamID == sid,
			sid == "" && bmID != "" && tp.BMID == bmID,
			sid == "" && bmID == "" && strings.EqualFold(tp.Name, name):
			continue
		}
		out = append(out, tp)
	}
	a.srv.Tracked = out
	a.saveLocked()
}

// LinkBMServer sets the BattleMetrics server id for the active server.
func (a *App) LinkBMServer(id string) error {
	id = ParseBMID(id)
	a.mu.Lock()
	if a.srv == nil {
		a.mu.Unlock()
		return errors.New("connect to a server first")
	}
	a.srv.BMServerID = id
	a.saveLocked()
	a.mu.Unlock()
	a.tracker.poke()
	return nil
}

// SetQueryHint stores the server address the user typed and re-detects the query port.
func (a *App) SetQueryHint(addr string) error {
	a.mu.Lock()
	if a.srv == nil {
		a.mu.Unlock()
		return errors.New("connect to a server first")
	}
	a.srv.QueryHint = strings.TrimSpace(addr)
	a.srv.QueryAddr = ""
	a.tracker.key = "" // force a fresh start
	a.saveLocked()
	a.mu.Unlock()
	a.tracker.poke()
	return nil
}

//go:embed data/randomnames.txt
var randomNamesRaw string

var randomNames = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range strings.Split(randomNamesRaw, "\n") {
		if n = strings.TrimSpace(n); n != "" {
			m[n] = true
		}
	}
	return m
}()

// isRandomName reports whether a name is one of Rust's stand-in names used when a server
// censors its player list (or a player uses streamer mode).
func isRandomName(n string) bool { return randomNames[strings.ToLower(strings.TrimSpace(n))] }
