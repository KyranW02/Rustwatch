package app

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rustwatch/internal/rustplus"
	"rustwatch/internal/sound"
)

//go:embed web
var webFS embed.FS

type ServerView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Img      string `json:"img"`
	Active   bool   `json:"active"`
	Devices  int    `json:"devices"`
	UseProxy bool   `json:"useProxy"`
}

type DeviceView struct {
	Device
	Value         *bool     `json:"value"`
	Items         []ItemQty `json:"items"`
	Capacity      int32     `json:"capacity"`
	HasProtection bool      `json:"hasProtection"`
	UpkeepLeft    string    `json:"upkeepLeft"`
	UpkeepSeconds int64     `json:"upkeepSeconds"`
	Error         string    `json:"error"`
	Updated       int64     `json:"updated"`
}

type ItemQty struct {
	ID        int32  `json:"id"`
	Name      string `json:"name"`
	Short     string `json:"short"`
	Quantity  int32  `json:"quantity"`
	Blueprint bool   `json:"blueprint"`
}

type MarkerView struct {
	rustplus.Marker
	Grid string `json:"grid"`
}

type StateView struct {
	Version     string            `json:"version"`
	Demo        bool              `json:"demo"`
	Status      string            `json:"status"`
	StatusError string            `json:"statusError"`
	Steam       map[string]any    `json:"steam"`
	Servers     []ServerView      `json:"servers"`
	Active      *ServerView       `json:"active"`
	Info        *rustplus.Info    `json:"info"`
	WipeAgo     string            `json:"wipeAgo"`
	Time        *TimeView         `json:"time"`
	Map         map[string]any    `json:"map"`
	Team        []MemberView      `json:"team"`
	LeaderID    string            `json:"leaderId"`
	MyID        string            `json:"myId"`
	Markers     []MarkerView      `json:"markers"`
	Events      *EventSummary     `json:"events"`
	Vending     []*VendingMachine `json:"vending"`
	Watches     []map[string]any  `json:"watches"`
	TrackedVMs  []string          `json:"trackedVMs"`
	Devices     []DeviceView      `json:"devices"`
	Chat        []ChatLine        `json:"chat"`
	Log         []LogEntry        `json:"log"`
	Timers      []map[string]any  `json:"timers"`
	Tracker     TrackerView       `json:"tracker"`
	Settings    Settings          `json:"settings"`
	AlertKinds  []map[string]any  `json:"alertKinds"`
	ConnectedAt int64             `json:"connectedAt"`
	DataDir     string            `json:"dataDir"`
	Notes       []rustplus.Note   `json:"notes"`
	Forecasts   []Forecast        `json:"forecasts"`
	Update      UpdateInfo        `json:"update"`
	MarkerInfo  MarkerDiag        `json:"markerInfo"`
	Sounds      SoundView         `json:"sounds"`
}

func (a *App) stateLocked() StateView {
	v := StateView{Version: Version, Demo: a.Demo, Status: a.status, StatusError: a.statusErr, Settings: a.cfg.Settings, DataDir: a.dir}
	linked := a.cfg.FCM != nil && a.cfg.FCM.AuthToken != ""
	v.Steam = map[string]any{"linked": linked, "status": a.fcmStatus, "pairing": a.pairing != nil}
	if linked {
		v.Steam["steamId"] = a.cfg.FCM.SteamID
	}
	for _, s := range a.cfg.Servers {
		sv := ServerView{ID: s.ID, Name: s.Name, IP: s.IP, Port: s.Port, Img: s.Img, Devices: len(s.Devices), UseProxy: s.UseProxy}
		sv.Active = a.srv != nil && a.srv.ID == s.ID
		if sv.Active {
			cp := sv
			v.Active = &cp
			v.MyID = s.PlayerID
		}
		v.Servers = append(v.Servers, sv)
	}
	for _, k := range alertKinds {
		v.AlertKinds = append(v.AlertKinds, map[string]any{"key": k.Key, "label": k.Label})
	}
	v.Tracker = a.tracker.viewLocked()
	v.Sounds = a.sounds.View()
	v.Update = a.update
	v.Update.Current, v.Update.Repo = Version, a.updateRepoLocked()
	v.Log = a.logs
	if len(v.Log) > 200 {
		v.Log = v.Log[len(v.Log)-200:]
	}
	if a.srv == nil {
		return v
	}
	if !a.connectedAt.IsZero() {
		v.ConnectedAt = a.connectedAt.Unix()
	}
	v.Info = a.info
	if a.info != nil && a.info.WipeTime > 0 {
		v.WipeAgo = fmtDur(time.Since(time.Unix(int64(a.info.WipeTime), 0)))
	}
	v.Time = a.timeViewLocked()
	if a.mapData != nil {
		v.Map = map[string]any{"width": a.mapData.Width, "height": a.mapData.Height, "oceanMargin": a.mapData.OceanMargin,
			"monuments": a.mapData.Monuments, "background": a.mapData.Background, "mapSize": a.info.MapSize,
			"monumentNames": monumentDisplay(a.mapData.Monuments)}
	}
	v.Team = a.teamViewLocked()
	if a.team != nil {
		v.LeaderID = strconv.FormatUint(a.team.LeaderSteamID, 10)
		v.Notes = append(append([]rustplus.Note{}, a.team.MapNotes...), a.team.LeaderNotes...)
	}
	var ms uint32 = 4000
	if a.info != nil {
		ms = a.info.MapSize
	}
	for _, m := range a.markers {
		if m.Type == rustplus.MarkerVendingMachine {
			continue
		}
		v.Markers = append(v.Markers, MarkerView{Marker: m, Grid: Grid(m.X, m.Y, ms)})
	}
	if a.events != nil {
		s := a.events.Summary()
		v.Events = &s
	}
	v.Forecasts = a.forecastsLocked()
	v.MarkerInfo = a.markerDiag
	if a.vending != nil {
		v.Vending = a.vending.list()
	}
	for _, w := range a.srv.Watches {
		v.Watches = append(v.Watches, map[string]any{"itemId": w.ItemID, "maxPrice": w.MaxPrice, "name": ItemName(w.ItemID), "short": ItemShort(w.ItemID)})
	}
	v.TrackedVMs = a.srv.TrackedVMs
	for _, d := range a.srv.Devices {
		dv := DeviceView{Device: d, Items: []ItemQty{}}
		if ds := a.devState[d.EntityID]; ds != nil {
			dv.Error = ds.Error
			if !ds.Updated.IsZero() {
				dv.Updated = ds.Updated.Unix()
			}
			if p := ds.Payload; p != nil {
				val := p.Value
				dv.Value = &val
				dv.Capacity = p.Capacity
				dv.HasProtection = p.HasProtection
				if p.HasProtection && p.ProtectionExpiry > 0 {
					left := time.Until(time.Unix(int64(p.ProtectionExpiry), 0))
					dv.UpkeepLeft, dv.UpkeepSeconds = fmtDur(left), int64(left.Seconds())
				}
				for _, it := range p.Items {
					dv.Items = append(dv.Items, ItemQty{ID: it.ItemID, Name: ItemName(it.ItemID), Short: ItemShort(it.ItemID), Quantity: it.Quantity, Blueprint: it.IsBlueprint})
				}
			}
		}
		v.Devices = append(v.Devices, dv)
	}
	v.Chat = a.chat
	for _, t := range a.timers {
		v.Timers = append(v.Timers, map[string]any{"id": t.ID, "message": t.Message, "fires": t.Fires.Unix(), "left": fmtDur(time.Until(t.Fires))})
	}
	return v
}

func monumentDisplay(ms []rustplus.Monument) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		out[m.Token] = monumentName(m.Token)
	}
	return out
}

// Listen picks the port (preferring the fixed one so the window URL stays stable).
func Listen(preferred int) (net.Listener, error) {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferred))
	if err == nil {
		return l, nil
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

func (a *App) Serve(l net.Listener) error {
	a.Port = l.Addr().(*net.TCPAddr).Port
	return http.Serve(l, a.Handler())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(400)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

// localOnly blocks cross-site requests from other web pages (DNS rebinding / CSRF).
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "forbidden", 403)
			return
		}
		if r.Method == "POST" {
			if o := r.Header.Get("Origin"); o != "" && !strings.HasPrefix(o, "http://127.0.0.1") && !strings.HasPrefix(o, "http://localhost") {
				http.Error(w, "forbidden", 403)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]string{"app": "RustWatch"}) })
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		v := a.stateLocked()
		b, err := json.Marshal(v)
		a.mu.Unlock()
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
	})
	mux.HandleFunc("GET /api/map.jpg", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		jpg := a.mapJPG
		a.mu.Unlock()
		if jpg == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpg)
	})
	mux.HandleFunc("GET /api/stream", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "stream unsupported", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		ch := a.subscribe()
		defer a.unsubscribe(ch)
		fmt.Fprint(w, ": hi\n\n")
		fl.Flush()
		ping := time.NewTicker(20 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case m := <-ch:
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.Event, m.Data)
				fl.Flush()
			case <-ping.C:
				fmt.Fprint(w, ": ping\n\n")
				fl.Flush()
			}
		}
	})

	// Steam link / pairing
	mux.HandleFunc("GET /pair/callback", func(w http.ResponseWriter, r *http.Request) {
		tok := r.URL.Query().Get("token")
		if tok == "" {
			http.Error(w, "Token missing", 400)
			return
		}
		if err := a.FinishSteamLink(tok, r.URL.Query().Get("steamId")); err != nil {
			http.Error(w, "Linking failed: "+err.Error(), 500)
			return
		}
		fmt.Fprint(w, "Steam linked! You can close this window. Now open Rust → ESC → Rust+ → Pair with Server.")
	})
	post := func(path string, fn func(r *http.Request) (any, error)) {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			v, err := fn(r)
			if err != nil {
				writeErr(w, err)
				return
			}
			if v == nil {
				v = map[string]bool{"ok": true}
			}
			writeJSON(w, v)
			go a.publish("update", nil)
		})
	}
	post("/api/steam/link", func(r *http.Request) (any, error) { return nil, a.StartSteamLink() })
	post("/api/steam/cancel", func(r *http.Request) (any, error) { a.CancelSteamLink(); return nil, nil })
	post("/api/steam/unlink", func(r *http.Request) (any, error) { a.UnlinkSteam(); return nil, nil })

	post("/api/servers/connect", func(r *http.Request) (any, error) {
		var b struct{ ID string }
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		return nil, a.ConnectServer(b.ID)
	})
	post("/api/servers/disconnect", func(r *http.Request) (any, error) { a.Disconnect(); return nil, nil })
	post("/api/servers/remove", func(r *http.Request) (any, error) {
		var b struct{ ID string }
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		a.RemoveServer(b.ID)
		return nil, nil
	})
	post("/api/servers/add", func(r *http.Request) (any, error) {
		var b struct {
			Name, IP, PlayerID string
			Port               int
			PlayerToken        string
			UseProxy           bool
		}
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		tok, err := strconv.ParseInt(strings.TrimSpace(b.PlayerToken), 10, 64)
		if b.IP == "" || b.Port == 0 || b.PlayerID == "" || err != nil {
			return nil, errors.New("IP, app port, Steam ID and player token are all required")
		}
		id := a.AddServer(ServerConfig{Name: b.Name, IP: strings.TrimSpace(b.IP), Port: b.Port, PlayerID: strings.TrimSpace(b.PlayerID), PlayerToken: int32(tok), UseProxy: b.UseProxy})
		return nil, a.ConnectServer(id)
	})
	post("/api/servers/proxy", func(r *http.Request) (any, error) {
		var b struct {
			ID       string
			UseProxy bool
		}
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		a.mu.Lock()
		sc := a.findServerLocked(b.ID)
		if sc != nil {
			sc.UseProxy = b.UseProxy
			a.saveLocked()
		}
		active := a.srv == sc
		a.mu.Unlock()
		if active && sc != nil {
			return nil, a.ConnectServer(b.ID)
		}
		return nil, nil
	})

	post("/api/chat/send", func(r *http.Request) (any, error) {
		var b struct{ Message string }
		if err := decode(r, &b); err != nil || strings.TrimSpace(b.Message) == "" {
			return nil, errors.New("empty message")
		}
		msg := strings.TrimSpace(b.Message)
		if err := a.SendRawChat(msg); err != nil {
			return nil, err
		}
		return nil, nil
	})
	post("/api/command", func(r *http.Request) (any, error) {
		var b struct{ Line string }
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		a.mu.Lock()
		pfx := a.cfg.Settings.Prefix
		var me uint64
		if a.srv != nil {
			me, _ = strconv.ParseUint(a.srv.PlayerID, 10, 64)
		}
		a.mu.Unlock()
		reply := a.Command(me, strings.TrimPrefix(strings.TrimSpace(b.Line), pfx))
		if reply == "" {
			reply = "Unknown command. Try " + pfx + "help"
		}
		return map[string]string{"reply": reply}, nil
	})

	// devices
	post("/api/devices/toggle", func(r *http.Request) (any, error) {
		var b struct {
			EntityID uint32
			Value    bool
		}
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		a.mu.Lock()
		c := a.client
		a.mu.Unlock()
		if c == nil {
			return nil, rustplus.ErrNotConnected
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		return nil, c.SetEntityValue(ctx, b.EntityID, b.Value)
	})
	post("/api/devices/save", func(r *http.Request) (any, error) {
		var d Device
		if err := decode(r, &d); err != nil {
			return nil, err
		}
		d.Command = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(d.Command), " ", ""))
		if builtinCommands[d.Command] {
			return nil, fmt.Errorf("'%s' is a built-in command", d.Command)
		}
		a.mu.Lock()
		sid := ""
		if a.srv != nil {
			sid = a.srv.ID
			for i := range a.srv.Devices {
				if a.srv.Devices[i].EntityID == d.EntityID {
					a.srv.Devices[i].Command = d.Command // allow clearing
				}
			}
		}
		a.mu.Unlock()
		if sid == "" {
			return nil, errors.New("connect to a server first")
		}
		a.AddDevice(sid, d)
		return nil, nil
	})
	post("/api/devices/remove", func(r *http.Request) (any, error) {
		var b struct{ EntityID uint32 }
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.srv != nil {
			var out []Device
			for _, d := range a.srv.Devices {
				if d.EntityID != b.EntityID {
					out = append(out, d)
				}
			}
			a.srv.Devices = out
			delete(a.devState, b.EntityID)
			a.saveLocked()
		}
		return nil, nil
	})
	post("/api/devices/refresh", func(r *http.Request) (any, error) {
		a.mu.Lock()
		c := a.client
		a.mu.Unlock()
		if c == nil {
			return nil, rustplus.ErrNotConnected
		}
		a.refreshDevices(r.Context(), c)
		return nil, nil
	})

	post("/api/team/promote", func(r *http.Request) (any, error) {
		var b struct{ SteamID string }
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		id, _ := strconv.ParseUint(b.SteamID, 10, 64)
		a.mu.Lock()
		c := a.client
		a.mu.Unlock()
		if c == nil {
			return nil, rustplus.ErrNotConnected
		}
		return nil, c.PromoteToLeader(r.Context(), id)
	})

	// vending
	post("/api/vending/watch", func(r *http.Request) (any, error) {
		var b struct {
			ItemID   int32
			MaxPrice int32
			Remove   bool
		}
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.srv == nil {
			return nil, errors.New("connect to a server first")
		}
		var out []VendingWatch
		for _, w := range a.srv.Watches {
			if w.ItemID != b.ItemID {
				out = append(out, w)
			}
		}
		if !b.Remove {
			out = append(out, VendingWatch{ItemID: b.ItemID, MaxPrice: b.MaxPrice})
		}
		a.srv.Watches = out
		a.saveLocked()
		return nil, nil
	})
	post("/api/vending/track", func(r *http.Request) (any, error) {
		var b struct {
			Key   string
			Track bool
		}
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.srv == nil {
			return nil, errors.New("connect to a server first")
		}
		var out []string
		for _, k := range a.srv.TrackedVMs {
			if k != b.Key {
				out = append(out, k)
			}
		}
		if b.Track {
			out = append(out, b.Key)
		}
		a.srv.TrackedVMs = out
		a.saveLocked()
		return nil, nil
	})
	mux.HandleFunc("GET /api/vending/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		buy := r.URL.Query().Get("mode") != "sell"
		it, ok := FindItem(q)
		if !ok {
			writeJSON(w, map[string]any{"item": nil, "hits": []any{}})
			return
		}
		a.mu.Lock()
		var hits []VendingHit
		if a.vending != nil {
			hits = a.vending.search(it.ID, buy)
		}
		a.mu.Unlock()
		writeJSON(w, map[string]any{"item": it, "hits": hits})
	})

	// items wiki
	mux.HandleFunc("GET /api/items/search", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, SearchItems(r.URL.Query().Get("q"), 25))
	})
	mux.HandleFunc("GET /api/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 32)
		d, ok := GetItemDetail(int32(id))
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, d)
	})

	// sound detection
	post("/api/sounds/capture", func(r *http.Request) (any, error) {
		id, x, err := a.sounds.Capture()
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "seconds": float64(len(x)) / sound.Rate, "wave": waveform(x, 900)}, nil
	})
	type cueSel struct {
		ID       string
		From, To float64
		Event    string
	}
	post("/api/sounds/test", func(r *http.Request) (any, error) {
		var b cueSel
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		return a.sounds.TestCue(b.ID, b.From, b.To)
	})
	post("/api/sounds/save", func(r *http.Request) (any, error) {
		var b cueSel
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		return a.sounds.SaveCue(b.ID, b.From, b.To, b.Event)
	})
	post("/api/sounds/delete", func(r *http.Request) (any, error) {
		var b cueSel
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		return nil, a.sounds.DeleteCue(b.ID)
	})
	mux.HandleFunc("GET /api/sounds/clip", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		from, _ := strconv.ParseFloat(q.Get("from"), 64)
		to, _ := strconv.ParseFloat(q.Get("to"), 64)
		x, err := a.sounds.clip(q.Get("id"), from, to)
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		if q.Get("img") != "" {
			w.Header().Set("Content-Type", "image/png")
			w.Write(spectrogramPNG(x))
			return
		}
		w.Write(sound.EncodeWAV(x, sound.Rate))
	})
	mux.HandleFunc("GET /api/sounds/cue", func(w http.ResponseWriter, r *http.Request) {
		b, err := a.sounds.cueAudio(r.URL.Query().Get("id"))
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(b)
	})

	// player tracker
	post("/api/tracker/track", func(r *http.Request) (any, error) {
		var tp TrackedPlayer
		if err := decode(r, &tp); err != nil {
			return nil, err
		}
		return nil, a.Track(tp)
	})
	post("/api/tracker/untrack", func(r *http.Request) (any, error) {
		var b struct{ BMID, Name, SteamID string }
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		a.Untrack(b.BMID, b.Name, b.SteamID)
		return nil, nil
	})
	post("/api/tracker/link", func(r *http.Request) (any, error) {
		var b struct{ ID string }
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		return nil, a.LinkBMServer(b.ID)
	})
	post("/api/tracker/address", func(r *http.Request) (any, error) {
		var b struct{ Addr string }
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		return nil, a.SetQueryHint(b.Addr)
	})
	post("/api/tracker/refresh", func(r *http.Request) (any, error) { a.tracker.poke(); return nil, nil })
	mux.HandleFunc("GET /api/tracker/servers", func(w http.ResponseWriter, r *http.Request) {
		res, err := a.tracker.SearchServers(r.Context(), r.URL.Query().Get("q"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, res)
	})

	// timers
	post("/api/timers/add", func(r *http.Request) (any, error) {
		var b struct{ Duration, Message string }
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		d, err := parseDuration(b.Duration)
		if err != nil || strings.TrimSpace(b.Message) == "" {
			return nil, errors.New("give a duration like 10m and a message")
		}
		a.mu.Lock()
		a.addTimerLocked(d, strings.TrimSpace(b.Message))
		a.mu.Unlock()
		return nil, nil
	})
	post("/api/timers/cancel", func(r *http.Request) (any, error) {
		var b struct{ ID int }
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.removeTimerLocked(b.ID)
		a.mu.Unlock()
		return nil, nil
	})

	// settings
	post("/api/settings", func(r *http.Request) (any, error) {
		var s Settings
		if err := decode(r, &s); err != nil {
			return nil, err
		}
		if s.Prefix == "" || len(s.Prefix) > 2 {
			return nil, errors.New("prefix must be 1-2 characters")
		}
		if s.DiscordWebhook != "" && !strings.HasPrefix(s.DiscordWebhook, "https://") {
			return nil, errors.New("Discord webhook must be an https:// URL")
		}
		a.mu.Lock()
		def := defaultSettings()
		if s.LockedCrateMinutes <= 0 {
			s.LockedCrateMinutes = def.LockedCrateMinutes
		}
		if s.CargoEgressMinutes <= 0 {
			s.CargoEgressMinutes = def.CargoEgressMinutes
		}
		if s.BradleyRespawnMin <= 0 {
			s.BradleyRespawnMin = def.BradleyRespawnMin
		}
		if s.AFKMinutes <= 0 {
			s.AFKMinutes = def.AFKMinutes
		}
		if s.BMPollSeconds < 30 {
			s.BMPollSeconds = 30
		}
		if s.Alerts == nil {
			s.Alerts = def.Alerts
		}
		if s.EventWindows == nil {
			s.EventWindows = def.EventWindows
		}
		for k, w := range s.EventWindows {
			if w[0] <= 0 || w[1] < w[0] || w[1] > 72 {
				return nil, fmt.Errorf("event timing for %s must be a range like 2 to 4 hours", k)
			}
		}
		old := a.cfg.Settings
		a.cfg.Settings = s
		a.applySettingsToEngineLocked()
		a.saveLocked()
		a.mu.Unlock()
		if old.SteamAPIKey != s.SteamAPIKey {
			select {
			case a.statsPoke <- struct{}{}:
			default:
			}
		}
		if old.BMToken != s.BMToken {
			a.tracker.poke()
		}
		return nil, nil
	})
	post("/api/notify/test", func(r *http.Request) (any, error) {
		a.mu.Lock()
		a.alertLockedForce("RustWatch test", "Desktop notifications are working")
		hook := a.cfg.Settings.DiscordWebhook
		srv := ""
		if a.srv != nil {
			srv = a.srv.Name
		}
		a.mu.Unlock()
		if hook != "" {
			if err := sendDiscord(hook, "RustWatch test", "Discord alerts are working", "rustplus", srv); err != nil {
				return nil, fmt.Errorf("discord: %w", err)
			}
		}
		return nil, nil
	})
	post("/api/events/reset-learning", func(r *http.Request) (any, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.srv != nil {
			a.srv.EventLog = map[string][]Stamp{}
			a.srv.Restarts = nil
			a.saveLocked()
		}
		return nil, nil
	})
	post("/api/log/clear", func(r *http.Request) (any, error) {
		a.mu.Lock()
		a.logs = nil
		a.mu.Unlock()
		return nil, nil
	})
	post("/api/datadir/open", func(r *http.Request) (any, error) {
		return nil, OpenFolder(a.dir)
	})
	post("/api/datadir/move", func(r *http.Request) (any, error) {
		var b struct{ Path string }
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		if a.Demo {
			return nil, errors.New("not available in demo mode")
		}
		if err := a.MoveDataDir(b.Path); err != nil {
			return nil, err
		}
		return nil, a.Restart()
	})
	post("/api/update/check", func(r *http.Request) (any, error) {
		return nil, a.CheckForUpdate(r.Context())
	})
	post("/api/update/install", func(r *http.Request) (any, error) {
		if a.Demo {
			return nil, errors.New("not available in demo mode")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		path, err := a.InstallUpdate(ctx)
		if err != nil {
			return nil, err
		}
		return nil, a.RestartWith(path)
	})
	post("/api/diagnostics", func(r *http.Request) (any, error) {
		path, err := a.SaveDiagnostics()
		if err != nil {
			return nil, err
		}
		OpenFolder(filepath.Dir(path))
		return map[string]string{"path": path}, nil
	})
	post("/api/window", func(r *http.Request) (any, error) {
		go a.OpenWindow()
		return nil, nil
	})
	post("/api/quit", func(r *http.Request) (any, error) {
		go func() {
			time.Sleep(300 * time.Millisecond)
			a.shutdown()
		}()
		return nil, nil
	})
	return localOnly(mux)
}
