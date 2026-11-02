package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rustwatch/internal/fcm"
	"rustwatch/internal/rustplus"
)

// Version is this build. Bump it for every release (tag the GitHub release v<Version>).
const Version = "1.1.0"

type LogEntry struct {
	Time  int64  `json:"time"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

type ChatLine struct {
	Time    int64  `json:"time"`
	SteamID string `json:"steamId"`
	Name    string `json:"name"`
	Message string `json:"message"`
	Color   string `json:"color"`
	Bot     bool   `json:"bot"`
}

type memberState struct {
	OnlineSince time.Time
	OfflineAt   time.Time
	LastMove    time.Time
	X, Y        float32
	AFK         bool
}

type ChatTimer struct {
	ID      int       `json:"id"`
	Message string    `json:"message"`
	Fires   time.Time `json:"fires"`
	cancel  context.CancelFunc
}

type sseMsg struct {
	Event string
	Data  []byte
}

type App struct {
	mu  sync.Mutex
	dir string
	cfg *Config

	saveCh chan []byte
	Port   int
	Demo   bool

	// active server session
	srv         *ServerConfig
	client      *rustplus.Client
	sessCancel  context.CancelFunc
	status      string
	statusErr   string
	info        *rustplus.Info
	gtime       *rustplus.Time
	timeAt      time.Time
	rateDay     float64
	rateNight   float64
	mapData     *rustplus.MapData
	mapJPG      []byte
	team        *rustplus.TeamInfo
	teamState   map[uint64]*memberState
	teamInit    bool
	markers     []rustplus.Marker
	events      *EventEngine
	vending     *VendingState
	devState    map[uint32]*deviceState
	chat        []ChatLine
	logs        []LogEntry
	timers      []*ChatTimer
	timerSeq    int
	sentRecent  map[string]time.Time
	connectedAt time.Time

	tracker *Tracker

	fcmStatus      string
	fcmCancel      context.CancelFunc
	pairing        *pairingSession
	subscribers    map[chan sseMsg]bool
	quit           chan struct{}
	alertLog       *dailyLog
	chatLog        *dailyLog
	stats          map[string]PlayerStats
	statsPoke      chan struct{}
	windowAlerted  map[string]int64
	update         UpdateInfo
	updateNotified string
}

type deviceState struct {
	Payload   *rustplus.Payload
	Error     string
	Updated   time.Time
	alarmTrig time.Time
	upkeepLow bool
}

func New(dir string) *App {
	st := loadStore(dir)
	a := &App{
		dir:         dir,
		cfg:         st.cfg,
		saveCh:      make(chan []byte, 1),
		status:      "disconnected",
		subscribers: map[chan sseMsg]bool{},
		quit:        make(chan struct{}),
		fcmStatus:   "not linked",
		alertLog:    newDailyLog(filepath.Join(dir, "logs"), "alerts"),
		chatLog:     newDailyLog(filepath.Join(dir, "logs"), "chat"),
		stats:       map[string]PlayerStats{},
		statsPoke:   make(chan struct{}, 1),
	}
	a.tracker = newTracker(a)
	go a.saver()
	return a
}

func (a *App) Quit() <-chan struct{} { return a.quit }

func (a *App) saver() {
	path := filepath.Join(a.dir, "config.json")
	for b := range a.saveCh {
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err == nil {
			os.Rename(tmp, path)
		} else {
			log.Println("save:", err)
		}
	}
}

// saveLocked snapshots the config; caller holds a.mu.
func (a *App) saveLocked() {
	b, err := json.MarshalIndent(a.cfg, "", "  ")
	if err != nil {
		return
	}
	select {
	case <-a.saveCh:
	default:
	}
	a.saveCh <- b
}

// Start launches background work: push listener, active server, ticker, tracker.
func (a *App) Start() {
	a.mu.Lock()
	if a.cfg.FCM != nil && a.cfg.FCM.AuthToken != "" {
		a.startListenerLocked()
	}
	active := a.cfg.ActiveServer
	if active == "" && len(a.cfg.Servers) > 0 {
		active = a.cfg.Servers[0].ID
	}
	a.mu.Unlock()
	if active != "" {
		a.ConnectServer(active)
	}
	go a.ticker()
	go a.tracker.run()
	go a.statsLoop()
	go a.updateLoop()
}

func (a *App) ticker() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		a.mu.Lock()
		if a.events != nil {
			a.events.Tick()
		}
		a.checkAFKLocked()
		a.checkWindowsLocked()
		now := time.Now()
		for k, v := range a.sentRecent {
			if now.Sub(v) > time.Minute {
				delete(a.sentRecent, k)
			}
		}
		a.mu.Unlock()
		a.publish("update", nil)
	}
}

// ---------- SSE ----------

func (a *App) subscribe() chan sseMsg {
	ch := make(chan sseMsg, 64)
	a.mu.Lock()
	a.subscribers[ch] = true
	a.mu.Unlock()
	return ch
}

func (a *App) unsubscribe(ch chan sseMsg) {
	a.mu.Lock()
	delete(a.subscribers, ch)
	a.mu.Unlock()
}

func (a *App) publish(event string, v any) {
	var data []byte
	if v != nil {
		data, _ = json.Marshal(v)
	} else {
		data = []byte("{}")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for ch := range a.subscribers {
		select {
		case ch <- sseMsg{event, data}:
		default:
		}
	}
}

// ---------- alerts ----------

// alertLocked records an alert and routes it. Caller holds a.mu.
func (a *App) alertLocked(kind, title, text string) {
	e := LogEntry{Time: time.Now().Unix(), Kind: kind, Title: title, Text: text}
	a.logs = append(a.logs, e)
	if len(a.logs) > 500 {
		a.logs = a.logs[len(a.logs)-500:]
	}
	srvName := ""
	if a.srv != nil {
		srvName = a.srv.Name
	}
	a.alertLog.Printf("[%s] %s — %s  (%s)", kind, title, text, srvName)
	route := a.cfg.Settings.Alerts[kind]

	if route.Chat && a.client != nil && a.status == "connected" {
		go a.SendChat(text)
	}
	if route.Discord && a.cfg.Settings.DiscordWebhook != "" {
		go sendDiscord(a.cfg.Settings.DiscordWebhook, title, text, kind, srvName)
	}
	go a.publish("alert", map[string]any{"entry": e, "sound": a.cfg.Settings.Sound, "desktop": route.Desktop})
}

// ---------- servers ----------

func (a *App) findServerLocked(id string) *ServerConfig {
	for _, s := range a.cfg.Servers {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// AddServer adds or updates a paired server; returns its id.
func (a *App) AddServer(s ServerConfig) string {
	if s.ID == "" {
		s.ID = fmt.Sprintf("%s:%d", s.IP, s.Port)
	}
	a.mu.Lock()
	existing := a.findServerLocked(s.ID)
	if existing != nil {
		existing.PlayerID, existing.PlayerToken = s.PlayerID, s.PlayerToken
		if s.Name != "" {
			existing.Name = s.Name
		}
		if s.Img != "" {
			existing.Img = s.Img
		}
		if s.URL != "" {
			existing.URL = s.URL
		}
		existing.UseProxy = s.UseProxy
	} else {
		cp := s
		if cp.Deaths == nil {
			cp.Deaths = map[string]int{}
		}
		a.cfg.Servers = append(a.cfg.Servers, &cp)
	}
	a.saveLocked()
	a.mu.Unlock()
	return s.ID
}

func (a *App) RemoveServer(id string) {
	a.mu.Lock()
	var out []*ServerConfig
	for _, s := range a.cfg.Servers {
		if s.ID != id {
			out = append(out, s)
		}
	}
	a.cfg.Servers = out
	wasActive := a.srv != nil && a.srv.ID == id
	if a.cfg.ActiveServer == id {
		a.cfg.ActiveServer = ""
	}
	a.saveLocked()
	a.mu.Unlock()
	if wasActive {
		a.Disconnect()
	}
}

func (a *App) Disconnect() {
	a.mu.Lock()
	if a.sessCancel != nil {
		a.sessCancel()
		a.sessCancel = nil
	}
	if a.client != nil {
		a.client.Close()
		a.client = nil
	}
	a.srv = nil
	a.status = "disconnected"
	a.mu.Unlock()
	a.publish("update", nil)
}

// ConnectServer makes id the active server and starts its session.
func (a *App) ConnectServer(id string) error {
	a.Disconnect()
	a.mu.Lock()
	sc := a.findServerLocked(id)
	if sc == nil {
		a.mu.Unlock()
		return fmt.Errorf("unknown server %s", id)
	}
	if sc.Deaths == nil {
		sc.Deaths = map[string]int{}
	}
	a.cfg.ActiveServer = id
	a.saveLocked()
	ctx, cancel := context.WithCancel(context.Background())
	a.sessCancel = cancel
	a.srv = sc
	a.resetSessionLocked()
	a.mu.Unlock()
	go a.session(ctx, sc)
	a.tracker.poke()
	return nil
}

func (a *App) resetSessionLocked() {
	a.info, a.gtime, a.mapData, a.mapJPG, a.team = nil, nil, nil, nil, nil
	a.teamState = map[uint64]*memberState{}
	a.teamInit = false
	a.markers = nil
	a.events = NewEventEngine()
	a.vending = newVendingState()
	a.devState = map[uint32]*deviceState{}
	a.chat = nil
	a.sentRecent = map[string]time.Time{}
	a.rateDay, a.rateNight = 0, 0
	a.applySettingsToEngineLocked()
}

func (a *App) applySettingsToEngineLocked() {
	if a.events == nil {
		return
	}
	st := a.cfg.Settings
	a.events.LockedCrate = time.Duration(st.LockedCrateMinutes) * time.Minute
	a.events.CargoEgress = time.Duration(st.CargoEgressMinutes) * time.Minute
	a.events.BradleyRespawn = time.Duration(st.BradleyRespawnMin) * time.Minute
}

func (a *App) setStatus(s, errText string) {
	a.mu.Lock()
	a.status, a.statusErr = s, errText
	a.mu.Unlock()
	a.publish("update", nil)
}

// serverClosed reports whether a dropped connection looks like the server going away
// (restart / shutdown) rather than our own network hiccup.
func serverClosed(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "reset") || strings.Contains(m, "forcibly closed") || strings.Contains(m, "connection closed") || strings.Contains(m, "refused")
}

func (a *App) session(ctx context.Context, sc *ServerConfig) {
	backoff := 3 * time.Second
	var droppedAt time.Time // when the server dropped us (possible restart)
	for ctx.Err() == nil {
		a.setStatus("connecting", "")
		pid, _ := strconv.ParseUint(sc.PlayerID, 10, 64)
		c := &rustplus.Client{Host: sc.IP, Port: sc.Port, PlayerID: pid, PlayerToken: sc.PlayerToken, UseProxy: sc.UseProxy}
		discon := make(chan error, 1)
		c.OnBroadcast = a.onBroadcast
		c.OnDisconnected = func(err error) { discon <- err }
		err := c.Connect()
		if err == nil {
			a.mu.Lock()
			a.client = c
			a.mu.Unlock()
			err = a.initSession(ctx, c)
			if err == nil {
				backoff = 3 * time.Second
				a.mu.Lock()
				a.connectedAt = time.Now()
				// back after the server was gone for a while: it restarted, so its event schedule did too
				if !droppedAt.IsZero() && time.Since(droppedAt) > 45*time.Second {
					a.recordRestartLocked(time.Now())
				}
				a.mu.Unlock()
				droppedAt = time.Time{}
				a.setStatus("connected", "")
				err = a.pollLoop(ctx, c, discon)
				if serverClosed(err) {
					droppedAt = time.Now()
				}
			}
			c.Close()
			a.mu.Lock()
			if a.client == c {
				a.client = nil
			}
			a.mu.Unlock()
		}
		if ctx.Err() != nil {
			return
		}
		msg := "connection lost"
		if err != nil {
			msg = err.Error()
			if strings.Contains(msg, "not_found") || strings.Contains(msg, "access") {
				msg += " — the pairing may be outdated, pair with the server again in-game"
			}
		}
		a.setStatus("reconnecting", msg)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func (a *App) initSession(ctx context.Context, c *rustplus.Client) error {
	info, err := c.GetInfo(ctx)
	if err != nil {
		return err
	}
	m, err := c.GetMap(ctx)
	if err != nil {
		return fmt.Errorf("map: %w", err)
	}
	a.mu.Lock()
	a.info = info
	a.mapData = m
	a.mapJPG = m.JPG
	if a.srv != nil && info.Name != "" && a.srv.Name != info.Name {
		a.srv.Name = info.Name
		a.saveLocked()
	}
	a.checkWipeLocked()
	a.events.MapSize = info.MapSize
	a.events.Monuments = m.Monuments
	a.vending.mapSize, a.vending.monuments = info.MapSize, m.Monuments
	a.mu.Unlock()
	if t, err := c.GetTime(ctx); err == nil {
		a.setTime(t)
	}
	if team, err := c.GetTeamInfo(ctx); err == nil {
		a.updateTeam(team)
	}
	if chat, err := c.GetTeamChat(ctx); err == nil { // also subscribes to team chat broadcasts
		a.mu.Lock()
		for _, m := range chat {
			a.chat = append(a.chat, a.chatLine(m))
		}
		a.mu.Unlock()
	}
	a.refreshDevices(ctx, c)
	if mk, err := c.GetMapMarkers(ctx); err == nil {
		a.updateMarkers(mk)
	}
	a.tracker.poke()
	select {
	case a.statsPoke <- struct{}{}:
	default:
	}
	return nil
}

func (a *App) pollLoop(ctx context.Context, c *rustplus.Client, discon chan error) error {
	fast := time.NewTicker(10 * time.Second)
	slow := time.NewTicker(45 * time.Second)
	devs := time.NewTicker(5 * time.Minute)
	defer fast.Stop()
	defer slow.Stop()
	defer devs.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-discon:
			if err == nil {
				err = fmt.Errorf("connection closed")
			}
			return err
		case <-fast.C:
			if team, err := c.GetTeamInfo(ctx); err == nil {
				a.updateTeam(team)
			}
			if mk, err := c.GetMapMarkers(ctx); err == nil {
				a.updateMarkers(mk)
			}
		case <-slow.C:
			if info, err := c.GetInfo(ctx); err == nil {
				a.mu.Lock()
				a.info = info
				a.checkWipeLocked()
				a.mu.Unlock()
			}
			if t, err := c.GetTime(ctx); err == nil {
				a.setTime(t)
			}
		case <-devs.C:
			a.refreshDevices(ctx, c)
		}
		a.publish("update", nil)
	}
}

func (a *App) refreshDevices(ctx context.Context, c *rustplus.Client) {
	a.mu.Lock()
	var devs []Device
	if a.srv != nil {
		devs = append(devs, a.srv.Devices...)
	}
	a.mu.Unlock()
	for _, d := range devs {
		ei, err := c.GetEntityInfo(ctx, d.EntityID)
		a.mu.Lock()
		ds := a.devState[d.EntityID]
		if ds == nil {
			ds = &deviceState{}
			a.devState[d.EntityID] = ds
		}
		if err != nil {
			ds.Error = err.Error()
		} else {
			p := ei.Payload
			ds.Payload, ds.Error, ds.Updated = &p, "", time.Now()
			a.checkUpkeepLocked(d, ds)
		}
		a.mu.Unlock()
	}
}

func (a *App) checkUpkeepLocked(d Device, ds *deviceState) {
	if d.Type != rustplus.EntityStorageMonitor || ds.Payload == nil || !ds.Payload.HasProtection || ds.Payload.ProtectionExpiry == 0 {
		return
	}
	left := time.Until(time.Unix(int64(ds.Payload.ProtectionExpiry), 0))
	lim := time.Duration(a.cfg.Settings.UpkeepAlertHours) * time.Hour
	if left < lim && !ds.upkeepLow {
		ds.upkeepLow = true
		a.alertLocked("storage_upkeep", "Upkeep running low", fmt.Sprintf("%s: base decays in %s — refill the TC!", d.Name, fmtDur(left)))
	} else if left >= lim {
		ds.upkeepLow = false
	}
}

func (a *App) onBroadcast(b *rustplus.Broadcast) {
	if b.TeamChanged != nil {
		a.updateTeam(b.TeamChanged)
	}
	if b.TeamMessage != nil {
		a.onTeamMessage(*b.TeamMessage)
	}
	if b.EntityChanged != nil {
		a.mu.Lock()
		id := b.EntityChanged.EntityID
		ds := a.devState[id]
		if ds == nil {
			ds = &deviceState{}
			a.devState[id] = ds
		}
		p := b.EntityChanged.Payload
		var dev *Device
		if a.srv != nil {
			for i := range a.srv.Devices {
				if a.srv.Devices[i].EntityID == id {
					dev = &a.srv.Devices[i]
				}
			}
		}
		if dev != nil && dev.Type == rustplus.EntityStorageMonitor && !p.HasValue && len(p.Items) == 0 && ds.Payload != nil {
			// partial update — keep previous contents
		} else {
			ds.Payload, ds.Error, ds.Updated = &p, "", time.Now()
		}
		if dev != nil && dev.Type == rustplus.EntityAlarm && p.Value && time.Since(ds.alarmTrig) > 10*time.Second {
			ds.alarmTrig = time.Now()
			a.alertLocked("alarm", "Alarm: "+dev.Name, fmt.Sprintf("Smart alarm '%s' was triggered!", dev.Name))
		}
		if dev != nil {
			a.checkUpkeepLocked(*dev, ds)
		}
		a.mu.Unlock()
	}
	a.publish("update", nil)
}

// ---------- time ----------

func (a *App) setTime(t *rustplus.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	if a.gtime != nil {
		dt := now.Sub(a.timeAt).Seconds()
		dg := float64(t.Time - a.gtime.Time)
		if dg < 0 {
			dg += 24
		}
		if dt > 20 && dg > 0 && dg < 6 {
			rate := dg / dt
			wasDay := isDay(a.gtime)
			if wasDay == isDay(t) {
				if wasDay {
					a.rateDay = rate
				} else {
					a.rateNight = rate
				}
			}
		}
	}
	a.gtime, a.timeAt = t, now
}

func isDay(t *rustplus.Time) bool { return t.Time >= t.Sunrise && t.Time < t.Sunset }

// game hours per real second
func (a *App) ratesLocked() (day, night float64) {
	t := a.gtime
	dayHours := float64(t.Sunset - t.Sunrise)
	dl := float64(t.DayLengthMinutes)
	if dl <= 0 {
		dl = 60
	}
	ts := float64(t.TimeScale)
	if ts <= 0 {
		ts = 1
	}
	day = dayHours / (dl * 60 * 0.75) * ts
	night = (24 - dayHours) / (dl * 60 * 0.25) * ts
	if a.rateDay > 0 {
		day = a.rateDay
	}
	if a.rateNight > 0 {
		night = a.rateNight
	}
	return
}

type TimeView struct {
	Clock      string  `json:"clock"`
	IsDay      bool    `json:"isDay"`
	UntilLabel string  `json:"untilLabel"`
	Until      string  `json:"until"`
	Hours      float64 `json:"hours"`
}

func (a *App) timeViewLocked() *TimeView {
	if a.gtime == nil {
		return nil
	}
	t := a.gtime
	day, night := a.ratesLocked()
	gt := float64(t.Time)
	el := time.Since(a.timeAt).Seconds()
	if isDay(t) {
		gt += el * day
	} else {
		gt += el * night
	}
	gt = math.Mod(gt, 24)
	cur := &rustplus.Time{Time: float32(gt), Sunrise: t.Sunrise, Sunset: t.Sunset}
	v := &TimeView{Hours: gt, IsDay: isDay(cur)}
	h := int(gt)
	m := int((gt - float64(h)) * 60)
	v.Clock = fmt.Sprintf("%02d:%02d", h, m)
	if v.IsDay {
		v.UntilLabel = "until sunset"
		v.Until = fmtDur(time.Duration((float64(t.Sunset) - gt) / day * float64(time.Second)))
	} else {
		left := float64(t.Sunrise) - gt
		if left < 0 {
			left += 24
		}
		v.UntilLabel = "until sunrise"
		v.Until = fmtDur(time.Duration(left / night * float64(time.Second)))
	}
	return v
}

// ---------- team ----------

func (a *App) updateTeam(t *rustplus.TeamInfo) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.srv == nil {
		return
	}
	now := time.Now()
	mapSize := uint32(4000)
	var mons []rustplus.Monument
	if a.info != nil {
		mapSize = a.info.MapSize
	}
	if a.mapData != nil {
		mons = a.mapData.Monuments
	}
	prev := map[uint64]rustplus.Member{}
	if a.team != nil {
		for _, m := range a.team.Members {
			prev[m.SteamID] = m
		}
	}
	for _, m := range t.Members {
		st := a.teamState[m.SteamID]
		if st == nil {
			st = &memberState{LastMove: now, X: m.X, Y: m.Y}
			if m.Online {
				st.OnlineSince = now
			}
			a.teamState[m.SteamID] = st
		}
		p, had := prev[m.SteamID]
		if a.teamInit && had {
			if m.Online && !p.Online {
				st.OnlineSince, st.LastMove, st.AFK = now, now, false
				a.alertLocked("team_online", m.Name+" is online", fmt.Sprintf("%s came online", m.Name))
			} else if !m.Online && p.Online {
				st.OfflineAt = now
				dur := ""
				if !st.OnlineSince.IsZero() {
					dur = " after " + fmtDur(now.Sub(st.OnlineSince))
				}
				a.alertLocked("team_offline", m.Name+" went offline", fmt.Sprintf("%s went offline%s", m.Name, dur))
			}
			if !m.Alive && p.Alive {
				a.srv.Deaths[strconv.FormatUint(m.SteamID, 10)]++
				a.saveLocked()
				a.alertLocked("team_death", m.Name+" died", fmt.Sprintf("%s died at %s", m.Name, Location(p.X, p.Y, mapSize, mons)))
			}
		} else if a.teamInit && !had {
			a.alertLocked("team_online", m.Name+" joined the team", m.Name+" joined the team")
		}
		if math.Abs(float64(m.X-st.X)) > 0.5 || math.Abs(float64(m.Y-st.Y)) > 0.5 || !m.Alive {
			if st.AFK && m.Online {
				st.AFK = false
			}
			st.LastMove, st.X, st.Y = now, m.X, m.Y
		}
	}
	if a.teamInit && a.team != nil {
		cur := map[uint64]bool{}
		for _, m := range t.Members {
			cur[m.SteamID] = true
		}
		for id, m := range prev {
			if !cur[id] {
				a.alertLocked("team_offline", m.Name+" left the team", m.Name+" left the team")
			}
		}
	}
	a.team = t
	a.teamInit = true
	a.checkAFKLocked()
}

func (a *App) checkAFKLocked() {
	if a.team == nil {
		return
	}
	lim := time.Duration(a.cfg.Settings.AFKMinutes) * time.Minute
	for _, m := range a.team.Members {
		st := a.teamState[m.SteamID]
		if st == nil || !m.Online || !m.Alive {
			continue
		}
		if !st.AFK && time.Since(st.LastMove) >= lim {
			st.AFK = true
			a.alertLocked("team_afk", m.Name+" is AFK", fmt.Sprintf("%s has been AFK for %s", m.Name, fmtDur(time.Since(st.LastMove))))
		}
	}
}

type MemberView struct {
	rustplus.Member
	Grid       string  `json:"grid"`
	Leader     bool    `json:"leader"`
	AFK        bool    `json:"afk"`
	AFKFor     string  `json:"afkFor"`
	OnlineFor  string  `json:"onlineFor"`
	OfflineFor string  `json:"offlineFor"`
	Deaths     int     `json:"deaths"`
	DeadFor    string  `json:"deadFor"`
	AliveFor   string  `json:"aliveFor"`
	Rotation   float32 `json:"rotation"`
	HasStats   bool    `json:"hasStats"`
	Kills      int64   `json:"kills"`
	LifeDeaths int64   `json:"lifeDeaths"`
	KD         string  `json:"kd"`
	WipeKills  int64   `json:"wipeKills"`
	WipeDeaths int64   `json:"wipeDeathsSteam"`
	Hours      int64   `json:"hours"`
	StatsError string  `json:"statsError"`
}

func (a *App) teamViewLocked() []MemberView {
	if a.team == nil {
		return nil
	}
	var mapSize uint32 = 4000
	if a.info != nil {
		mapSize = a.info.MapSize
	}
	var mons []rustplus.Monument
	if a.mapData != nil {
		mons = a.mapData.Monuments
	}
	now := time.Now()
	var out []MemberView
	for _, m := range a.team.Members {
		v := MemberView{Member: m, Leader: m.SteamID == a.team.LeaderSteamID}
		v.Grid = Location(m.X, m.Y, mapSize, mons)
		if st := a.teamState[m.SteamID]; st != nil {
			v.AFK = st.AFK && m.Online
			if v.AFK {
				v.AFKFor = fmtDur(now.Sub(st.LastMove))
			}
			if m.Online && !st.OnlineSince.IsZero() {
				v.OnlineFor = fmtDur(now.Sub(st.OnlineSince))
			}
			if !m.Online && !st.OfflineAt.IsZero() {
				v.OfflineFor = fmtDur(now.Sub(st.OfflineAt))
			}
		}
		if a.srv != nil {
			v.Deaths = a.srv.Deaths[strconv.FormatUint(m.SteamID, 10)]
		}
		if !m.Alive && m.DeathTime > 0 {
			v.DeadFor = fmtDur(now.Sub(time.Unix(int64(m.DeathTime), 0)))
		}
		if m.Alive && m.SpawnTime > 0 {
			v.AliveFor = fmtDur(now.Sub(time.Unix(int64(m.SpawnTime), 0)))
		}
		for _, mk := range a.markers {
			if mk.Type == rustplus.MarkerPlayer && mk.SteamID == m.SteamID {
				v.Rotation = mk.Rotation
			}
		}
		sid := strconv.FormatUint(m.SteamID, 10)
		if ps, ok := a.stats[sid]; ok {
			v.StatsError = ps.Error
			if !ps.Updated.IsZero() {
				v.HasStats = true
				v.Kills, v.LifeDeaths, v.Hours = ps.Kills, ps.Deaths, int64(ps.Hours)
				if ps.Deaths > 0 {
					v.KD = strconv.FormatFloat(float64(ps.Kills)/float64(ps.Deaths), 'f', 2, 64)
				} else {
					v.KD = strconv.FormatInt(ps.Kills, 10)
				}
				if a.srv != nil {
					if b, ok := a.srv.StatBase[sid]; ok {
						v.WipeKills, v.WipeDeaths = ps.Kills-b[0], ps.Deaths-b[1]
					}
				}
			}
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Online != out[j].Online {
			return out[i].Online
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// ---------- markers ----------

func (a *App) updateMarkers(mk []rustplus.Marker) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.markers = mk
	a.events.Emit = a.alertLocked
	a.events.OnSpawn = a.recordSpawnLocked
	a.events.Update(mk)
	a.vending.update(mk, a)
}

// ---------- team chat ----------

func (a *App) chatLine(m rustplus.TeamMessage) ChatLine {
	bot := strings.HasPrefix(m.Message, a.cfg.Settings.BotTag) && a.cfg.Settings.BotTag != ""
	return ChatLine{Time: int64(m.Time), SteamID: strconv.FormatUint(m.SteamID, 10), Name: m.Name, Message: m.Message, Color: m.Color, Bot: bot}
}

func (a *App) onTeamMessage(m rustplus.TeamMessage) {
	a.mu.Lock()
	line := a.chatLine(m)
	if line.Time == 0 {
		line.Time = time.Now().Unix()
	}
	a.chat = append(a.chat, line)
	a.chatLog.Printf("%s: %s", m.Name, m.Message)
	if len(a.chat) > 300 {
		a.chat = a.chat[len(a.chat)-300:]
	}
	_, sentByUs := a.sentRecent[m.Message]
	st := a.cfg.Settings
	isCmd := st.ChatCommands && !line.Bot && !sentByUs && strings.HasPrefix(m.Message, st.Prefix)
	a.mu.Unlock()
	a.publish("update", nil)
	if isCmd {
		go a.runCommand(m, strings.TrimPrefix(m.Message, st.Prefix))
	}
}

// SendChat sends a message (split into chunks) to in-game team chat, tagged as the bot.
func (a *App) SendChat(text string) error {
	a.mu.Lock()
	c := a.client
	tag := a.cfg.Settings.BotTag
	a.mu.Unlock()
	if c == nil {
		return rustplus.ErrNotConnected
	}
	for _, chunk := range splitChat(text, 120) {
		msg := chunk
		if tag != "" {
			msg = tag + " " + chunk
		}
		a.mu.Lock()
		if a.sentRecent == nil {
			a.sentRecent = map[string]time.Time{}
		}
		a.sentRecent[msg] = time.Now()
		a.mu.Unlock()
		if err := c.SendTeamMessage(context.Background(), msg); err != nil {
			return err
		}
	}
	return nil
}

// SendRawChat sends exactly what the user typed from the app.
func (a *App) SendRawChat(text string) error {
	a.mu.Lock()
	c := a.client
	a.mu.Unlock()
	if c == nil {
		return rustplus.ErrNotConnected
	}
	return c.SendTeamMessage(context.Background(), text)
}

func splitChat(s string, n int) []string {
	var out []string
	for _, part := range strings.Split(s, "\n") {
		part = strings.TrimSpace(part)
		for len([]rune(part)) > n {
			r := []rune(part)
			cut := n
			for i := n; i > n/2; i-- {
				if r[i] == ' ' || r[i] == '|' || r[i] == ',' {
					cut = i
					break
				}
			}
			out = append(out, strings.TrimSpace(string(r[:cut])))
			part = strings.TrimLeft(string(r[cut:]), " |,")
		}
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// ---------- FCM push handling ----------

func (a *App) startListenerLocked() {
	if a.fcmCancel != nil {
		a.fcmCancel()
	}
	creds := a.cfg.FCM
	ctx, cancel := context.WithCancel(context.Background())
	a.fcmCancel = cancel
	a.fcmStatus = "connecting"
	l := &fcm.Listener{
		AndroidID:     fcm.ParseUint64(creds.AndroidID),
		SecurityToken: fcm.ParseUint64(creds.SecurityToken),
		OnPush:        a.onPush,
		OnStatus: func(ok bool, err error) {
			a.mu.Lock()
			if ok {
				a.fcmStatus = "listening"
			} else if err != nil {
				a.fcmStatus = "reconnecting: " + err.Error()
			}
			a.mu.Unlock()
			a.publish("update", nil)
		},
	}
	go l.Run(ctx)
}

func (a *App) onPush(p fcm.Push) {
	body := p.Data["body"]
	channel := p.Data["channelId"]
	var data map[string]string
	if body != "" {
		var raw map[string]any
		if json.Unmarshal([]byte(body), &raw) == nil {
			data = map[string]string{}
			for k, v := range raw {
				data[k] = fmt.Sprint(v)
			}
		}
	}
	if data != nil && channel == "pairing" || (data != nil && (data["type"] == "server" || data["type"] == "entity")) {
		a.handlePairing(data)
		return
	}
	title, msg := p.Data["title"], p.Data["message"]
	if title == "" && msg == "" {
		return
	}
	a.mu.Lock()
	kind := "rustplus"
	if channel == "alarm" {
		kind = "alarm"
	}
	a.alertLocked(kind, title, strings.TrimSpace(title+" "+msg))
	a.mu.Unlock()
}

func (a *App) handlePairing(d map[string]string) {
	port, _ := strconv.Atoi(d["port"])
	tok, _ := strconv.ParseInt(d["playerToken"], 10, 64)
	sc := ServerConfig{Name: d["name"], IP: d["ip"], Port: port, PlayerID: d["playerId"], PlayerToken: int32(tok),
		Img: d["img"], URL: d["url"], Desc: d["desc"]}
	id := a.AddServer(sc)
	switch d["type"] {
	case "server":
		a.mu.Lock()
		a.alertLocked("rustplus", "Server paired", "Paired with "+d["name"])
		active := a.srv != nil && a.srv.ID == id
		a.mu.Unlock()
		if !active {
			a.ConnectServer(id)
		}
	case "entity":
		eid, _ := strconv.ParseUint(d["entityId"], 10, 32)
		et, _ := strconv.Atoi(d["entityType"])
		name := d["entityName"]
		if name == "" {
			name = entityTypeName(int32(et))
		}
		a.AddDevice(id, Device{EntityID: uint32(eid), Type: int32(et), Name: name})
		a.mu.Lock()
		a.alertLocked("rustplus", "Device paired", fmt.Sprintf("Paired %s (%d)", name, eid))
		a.mu.Unlock()
	}
	a.publish("update", nil)
}

func entityTypeName(t int32) string {
	switch t {
	case rustplus.EntitySwitch:
		return "Smart Switch"
	case rustplus.EntityAlarm:
		return "Smart Alarm"
	case rustplus.EntityStorageMonitor:
		return "Storage Monitor"
	}
	return "Device"
}

// AddDevice adds/updates a device on server sid and subscribes to it.
func (a *App) AddDevice(sid string, d Device) {
	a.mu.Lock()
	sc := a.findServerLocked(sid)
	if sc == nil {
		a.mu.Unlock()
		return
	}
	found := false
	for i := range sc.Devices {
		if sc.Devices[i].EntityID == d.EntityID {
			if d.Name != "" {
				sc.Devices[i].Name = d.Name
			}
			if d.Type != 0 {
				sc.Devices[i].Type = d.Type
			}
			if d.Command != "" {
				sc.Devices[i].Command = d.Command
			}
			found = true
		}
	}
	if !found {
		sc.Devices = append(sc.Devices, d)
	}
	a.saveLocked()
	c := a.client
	active := a.srv == sc
	a.mu.Unlock()
	if active && c != nil {
		go func() {
			ei, err := c.GetEntityInfo(context.Background(), d.EntityID)
			a.mu.Lock()
			ds := a.devState[d.EntityID]
			if ds == nil {
				ds = &deviceState{}
				a.devState[d.EntityID] = ds
			}
			if err != nil {
				ds.Error = err.Error()
			} else {
				p := ei.Payload
				ds.Payload, ds.Error, ds.Updated = &p, "", time.Now()
				if d.Type == 0 {
					for i := range sc.Devices {
						if sc.Devices[i].EntityID == d.EntityID {
							sc.Devices[i].Type = ei.Type
							if sc.Devices[i].Name == "" {
								sc.Devices[i].Name = entityTypeName(ei.Type)
							}
						}
					}
					a.saveLocked()
				}
			}
			a.mu.Unlock()
			a.publish("update", nil)
		}()
	}
}
