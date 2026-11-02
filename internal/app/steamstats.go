package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Rust+ has no kill data, but Steam keeps lifetime Rust stats per player (kill_player, deaths, …).
// With a free Steam Web API key we read those for teammates. Kills "this wipe" are the difference
// from the first reading RustWatch took after the wipe (Steam stats count every server you play on).

var steamAPIBase = "https://api.steampowered.com"

type PlayerStats struct {
	Kills     int64     `json:"kills"`
	Deaths    int64     `json:"deaths"`
	Headshots int64     `json:"headshots"`
	Hours     float64   `json:"hours"`
	Error     string    `json:"error"`
	Updated   time.Time `json:"-"`
}

var statsHTTP = &http.Client{Timeout: 20 * time.Second}

func fetchSteamStats(ctx context.Context, key, steamID string) (PlayerStats, error) {
	var ps PlayerStats
	u := fmt.Sprintf("%s/ISteamUserStats/GetUserStatsForGame/v2/?appid=252490&key=%s&steamid=%s", steamAPIBase, url.QueryEscape(key), url.QueryEscape(steamID))
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	resp, err := statsHTTP.Do(req)
	if err != nil {
		return ps, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return ps, errors.New("Steam rejected the API key — check it in Settings")
	case resp.StatusCode != 200:
		return ps, errors.New("stats are private (Steam profile game details must be public)")
	}
	var out struct {
		PlayerStats struct {
			Stats []struct {
				Name  string  `json:"name"`
				Value float64 `json:"value"`
			} `json:"stats"`
		} `json:"playerstats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ps, err
	}
	for _, s := range out.PlayerStats.Stats {
		switch s.Name {
		case "kill_player":
			ps.Kills = int64(s.Value)
		case "deaths":
			ps.Deaths = int64(s.Value)
		case "headshot":
			ps.Headshots = int64(s.Value)
		}
	}
	// hours played (optional; needs the same public game details)
	u2 := fmt.Sprintf("%s/IPlayerService/GetOwnedGames/v1/?key=%s&steamid=%s&include_played_free_games=1&appids_filter%%5B0%%5D=252490", steamAPIBase, url.QueryEscape(key), url.QueryEscape(steamID))
	if req2, err := http.NewRequestWithContext(ctx, "GET", u2, nil); err == nil {
		if r2, err := statsHTTP.Do(req2); err == nil {
			var g struct {
				Response struct {
					Games []struct {
						AppID    int     `json:"appid"`
						Playtime float64 `json:"playtime_forever"`
					} `json:"games"`
				} `json:"response"`
			}
			json.NewDecoder(r2.Body).Decode(&g)
			r2.Body.Close()
			for _, x := range g.Response.Games {
				if x.AppID == 252490 {
					ps.Hours = x.Playtime / 60
				}
			}
		}
	}
	return ps, nil
}

// statsLoop refreshes teammates' Steam stats every few minutes.
func (a *App) statsLoop() {
	for {
		a.refreshStats()
		select {
		case <-a.statsPoke:
			time.Sleep(3 * time.Second)
		case <-time.After(4 * time.Minute):
		}
	}
}

func (a *App) refreshStats() {
	a.mu.Lock()
	key := a.cfg.Settings.SteamAPIKey
	sc := a.srv
	var ids []string
	if a.team != nil {
		for _, m := range a.team.Members {
			ids = append(ids, strconv.FormatUint(m.SteamID, 10))
		}
	}
	a.mu.Unlock()
	if key == "" || sc == nil || len(ids) == 0 {
		return
	}
	for _, id := range ids {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		ps, err := fetchSteamStats(ctx, key, id)
		cancel()
		a.mu.Lock()
		if err != nil {
			prev := a.stats[id]
			prev.Error = err.Error()
			a.stats[id] = prev
		} else {
			ps.Updated = time.Now()
			a.stats[id] = ps
			if a.srv == sc {
				if sc.StatBase == nil {
					sc.StatBase = map[string][2]int64{}
				}
				base, ok := sc.StatBase[id]
				if !ok || ps.Kills < base[0] {
					sc.StatBase[id] = [2]int64{ps.Kills, ps.Deaths}
					a.saveLocked()
				}
			}
		}
		a.mu.Unlock()
	}
	a.publish("update", nil)
}

// checkWipeLocked resets per-wipe counters when the server wipes.
func (a *App) checkWipeLocked() {
	if a.srv == nil || a.info == nil || a.info.WipeTime == 0 {
		return
	}
	if a.srv.WipeSeen != a.info.WipeTime {
		a.srv.WipeSeen = a.info.WipeTime
		a.srv.Deaths = map[string]int{}
		a.srv.StatBase = map[string][2]int64{}
		a.saveLocked()
		select {
		case a.statsPoke <- struct{}{}:
		default:
		}
	}
}
