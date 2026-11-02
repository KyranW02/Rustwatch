package fakerust

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	mux.HandleFunc("/IPlayerService/GetOwnedGames/v1/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"games": []any{map[string]any{"appid": 252490, "playtime_forever": 152340}}}})
	})
	return mux
}
