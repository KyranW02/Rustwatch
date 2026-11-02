package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"rustwatch/internal/fcm"
)

// Alert kinds. Each can be routed to desktop / team chat / Discord.
var alertKinds = []struct {
	Key, Label string
	Desktop    bool
	Chat       bool
}{
	{"team_online", "Teammate came online", true, false},
	{"team_offline", "Teammate went offline", true, false},
	{"team_death", "Teammate died", true, false},
	{"team_afk", "Teammate went AFK", false, false},
	{"tracked_online", "Tracked player joined", true, true},
	{"tracked_offline", "Tracked player left", true, true},
	{"alarm", "Smart alarm triggered", true, true},
	{"storage_upkeep", "TC upkeep running low", true, true},
	{"cargo", "Cargo ship (spawn / dock / leave)", true, true},
	{"heli", "Patrol helicopter (spawn / down / leave)", true, true},
	{"chinook", "Chinook / locked crate drop", true, true},
	{"oilrig", "Oil rig triggered / crate unlocked", true, true},
	{"bradley", "Bradley destroyed / respawn", true, true},
	{"vendor", "Travelling vendor", true, true},
	{"raid", "Raid heard (explosions, with estimated grid)", true, true},
	{"excavator", "Giant excavator started (heard)", true, true},
	{"deepsea", "Deep Sea alarm (heard)", true, true},
	{"event_window", "Heli / cargo / chinook due any minute", true, false},
	{"vending_new", "New vending machine", false, false},
	{"vending_watch", "Watched item for sale", true, true},
	{"vending_tracked", "Tracked shop changed", true, false},
	{"rustplus", "Other Rust+ notifications", true, false},
	{"update", "RustWatch update available", true, false},
}

type AlertRoute struct {
	Desktop bool `json:"desktop"`
	Chat    bool `json:"chat"`
	Discord bool `json:"discord"`
}

type Settings struct {
	Prefix             string                `json:"prefix"`
	ChatCommands       bool                  `json:"chatCommands"`
	BotTag             string                `json:"botTag"`
	DiscordWebhook     string                `json:"discordWebhook"`
	Sound              bool                  `json:"sound"`
	LockedCrateMinutes int                   `json:"lockedCrateMinutes"`
	CargoEgressMinutes int                   `json:"cargoEgressMinutes"`
	BradleyRespawnMin  int                   `json:"bradleyRespawnMinutes"`
	AFKMinutes         int                   `json:"afkMinutes"`
	UpkeepAlertHours   int                   `json:"upkeepAlertHours"`
	BMToken            string                `json:"bmToken"`
	SteamAPIKey        string                `json:"steamApiKey"`
	BMPollSeconds      int                   `json:"bmPollSeconds"`
	KeepRunning        bool                  `json:"keepRunning"`
	Alerts             map[string]AlertRoute `json:"alerts"`
	EventWindows       map[string][2]float64 `json:"eventWindows"`  // default hours between spawns (min, max)
	UpdateRepo         string                `json:"updateRepo"`    // GitHub owner/repo for updates ("" = built-in)
	NoUpdateCheck      bool                  `json:"noUpdateCheck"` // turn off automatic update checks
	SoundOff           bool                  `json:"soundOff"`      // turn off in-game sound detection
	RaidOff            bool                  `json:"raidOff"`       // turn off raid (explosion) detection
}

func defaultSettings() Settings {
	s := Settings{
		Prefix: "!", ChatCommands: true, BotTag: "[RW]", Sound: true,
		LockedCrateMinutes: 15, CargoEgressMinutes: 50, BradleyRespawnMin: 60, AFKMinutes: 5,
		UpkeepAlertHours: 2, BMPollSeconds: 60, Alerts: map[string]AlertRoute{}, EventWindows: defaultWindows(),
	}
	for _, k := range alertKinds {
		s.Alerts[k.Key] = AlertRoute{Desktop: k.Desktop, Chat: k.Chat}
	}
	return s
}

type Device struct {
	EntityID uint32 `json:"entityId"`
	Type     int32  `json:"type"` // 1 switch, 2 alarm, 3 storage monitor
	Name     string `json:"name"`
	Command  string `json:"command"` // chat alias for switches, e.g. "turrets"
}

type VendingWatch struct {
	ItemID   int32 `json:"itemId"`
	MaxPrice int32 `json:"maxPrice"` // 0 = any
}

type Session struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"` // 0 while online
}

type TrackedPlayer struct {
	BMID     string    `json:"bmId"`
	SteamID  string    `json:"steamId"` // followed by Steam presence when set
	Name     string    `json:"name"`
	Tag      string    `json:"tag"`
	Color    string    `json:"color"`
	Notes    string    `json:"notes"`
	Notify   bool      `json:"notify"`
	LastSeen int64     `json:"lastSeen"`
	Sessions []Session `json:"sessions"`
}

type ServerConfig struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	IP          string              `json:"ip"`
	Port        int                 `json:"port"`
	PlayerID    string              `json:"playerId"`
	PlayerToken int32               `json:"playerToken"`
	Img         string              `json:"img"`
	URL         string              `json:"url"`
	Desc        string              `json:"desc"`
	UseProxy    bool                `json:"useProxy"`
	BMServerID  string              `json:"bmServerId"`
	QueryAddr   string              `json:"queryAddr"` // detected Steam query address (ip:port)
	QueryHint   string              `json:"queryHint"` // address the user typed, used for detection
	WipeSeen    uint32              `json:"wipeSeen"`
	EventLog    map[string][]Stamp  `json:"eventLog"`     // sightings per event kind, for spawn forecasts
	Restarts    []int64             `json:"restarts"`     // detected server restarts (event schedules reset)
	StatBase    map[string][2]int64 `json:"statBaseline"` // steamId -> kills, deaths at wipe start
	Devices     []Device            `json:"devices"`
	Watches     []VendingWatch      `json:"vendingWatches"`
	TrackedVMs  []string            `json:"trackedVendingMachines"` // "x,y" keys
	Tracked     []TrackedPlayer     `json:"trackedPlayers"`
	Deaths      map[string]int      `json:"deaths"` // steamId -> count (this wipe)
}

type Config struct {
	FCM          *fcm.Credentials `json:"fcm"`
	Servers      []*ServerConfig  `json:"servers"`
	ActiveServer string           `json:"activeServer"`
	Settings     Settings         `json:"settings"`
}

type store struct {
	mu   sync.Mutex
	path string
	cfg  *Config
}

func loadStore(dir string) *store {
	os.MkdirAll(dir, 0o755)
	s := &store{path: filepath.Join(dir, "config.json"), cfg: &Config{Settings: defaultSettings()}}
	if b, err := os.ReadFile(s.path); err == nil {
		json.Unmarshal(b, s.cfg)
	}
	def := defaultSettings()
	st := &s.cfg.Settings
	if st.Prefix == "" {
		st.Prefix = def.Prefix
	}
	if st.LockedCrateMinutes <= 0 {
		st.LockedCrateMinutes = def.LockedCrateMinutes
	}
	if st.CargoEgressMinutes <= 0 {
		st.CargoEgressMinutes = def.CargoEgressMinutes
	}
	if st.BradleyRespawnMin <= 0 {
		st.BradleyRespawnMin = def.BradleyRespawnMin
	}
	if st.AFKMinutes <= 0 {
		st.AFKMinutes = def.AFKMinutes
	}
	if st.BMPollSeconds < 30 {
		st.BMPollSeconds = def.BMPollSeconds
	}
	if st.Alerts == nil {
		st.Alerts = map[string]AlertRoute{}
	}
	if st.EventWindows == nil {
		st.EventWindows = map[string][2]float64{}
	}
	for k, v := range defaultWindows() {
		if w, ok := st.EventWindows[k]; !ok || w[1] <= 0 || w[0] > w[1] {
			st.EventWindows[k] = v
		}
	}
	for k, v := range def.Alerts {
		if _, ok := st.Alerts[k]; !ok {
			st.Alerts[k] = v
		}
	}
	return s
}

// save writes the config atomically. Caller must hold no lock that save needs.
func (s *store) save() error {
	s.mu.Lock()
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
