package fakerust

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// BattleMetrics returns a handler imitating the parts of api.battlemetrics.com the app uses.
func (s *Server) BattleMetrics() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "424242", "attributes": map[string]any{
			"name": "[EU] RustWatch Demo Server | Monthly", "ip": "127.0.0.1", "port": 28015, "players": 147, "maxPlayers": 200, "status": "online"}}}})
	})
	mux.HandleFunc("/servers/", func(w http.ResponseWriter, r *http.Request) {
		var inc []any
		for _, x := range s.onlinePlayers() {
			inc = append(inc, map[string]any{"type": "player", "id": x.id, "attributes": map[string]any{"name": x.name}})
		}
		id := strings.TrimPrefix(r.URL.Path, "/servers/")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"id": id, "attributes": map[string]any{"name": "[EU] RustWatch Demo Server | Monthly",
				"players": 147, "maxPlayers": 200, "details": map[string]any{"rust_queued_players": 3}}},
			"included": inc,
		})
	})
	return mux
}

var _ = fmt.Sprint

// SteamStats imitates the Steam Web API stats endpoints for the demo.
func (s *Server) SteamStats() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ISteamUserStats/GetUserStatsForGame/v2/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("steamid")
		if strings.HasSuffix(id, "3") {
			http.Error(w, "private", 500) // Alice keeps her stats private
			return
		}
		el := int(time.Since(s.start).Minutes())
		k := map[string]int{"76561198000000001": 1843, "76561198000000002": 412}[id] + el
		json.NewEncoder(w).Encode(map[string]any{"playerstats": map[string]any{"stats": []any{
			map[string]any{"name": "kill_player", "value": k},
			map[string]any{"name": "deaths", "value": k * 3 / 4},
			map[string]any{"name": "headshot", "value": k / 3},
		}}})
	})
	// Steam presence: "Ghost" alternates between this server and another one every 90 s,
	// "Hermit" has a private profile, vanity "ghost" resolves to Ghost.
	mux.HandleFunc("/ISteamUser/GetPlayerSummaries/v2/", func(w http.ResponseWriter, r *http.Request) {
		host, port := "127.0.0.1", 0
		if h, p, err := net.SplitHostPort(s.Addr); err == nil {
			host = h
			port, _ = strconv.Atoi(p)
		}
		var players []any
		for _, id := range strings.Split(r.URL.Query().Get("steamids"), ",") {
			switch id {
			case "76561198000000077":
				gs := fmt.Sprintf("%s:%d", host, port-67)
				if int(time.Since(s.start).Seconds()/90)%2 == 1 {
					gs = "203.0.113.50:28015"
				}
				players = append(players, map[string]any{"steamid": id, "personaname": "Ghost", "personastate": 1,
					"communityvisibilitystate": 3, "gameid": "252490", "gameserverip": gs})
			case "76561198000000078":
				players = append(players, map[string]any{"steamid": id, "personaname": "Hermit", "personastate": 0, "communityvisibilitystate": 1})
			default:
				if strings.HasPrefix(id, "7656119") {
					players = append(players, map[string]any{"steamid": id, "personaname": "Player " + id[len(id)-4:], "personastate": 1, "communityvisibilitystate": 3})
				}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"players": players}})
	})
	mux.HandleFunc("/ISteamUser/ResolveVanityURL/v1/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("vanityurl") == "ghost" {
			json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"success": 1, "steamid": "76561198000000077"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"success": 42}})
	})
	mux.HandleFunc("/IPlayerService/GetOwnedGames/v1/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"games": []any{map[string]any{"appid": 252490, "playtime_forever": 152340}}}})
	})
	return mux
}
