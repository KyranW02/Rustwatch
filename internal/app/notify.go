package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"
)

var kindColors = map[string]int{
	"team_online": 0x3ba55c, "team_offline": 0x99aab5, "team_death": 0xed4245, "team_afk": 0xfaa61a,
	"tracked_online": 0x57f287, "tracked_offline": 0x747f8d, "alarm": 0xed4245, "storage_upkeep": 0xfaa61a,
	"cargo": 0x5865f2, "heli": 0xeb459e, "chinook": 0xfee75c, "oilrig": 0xe67e22, "bradley": 0x992d22,
	"vendor": 0x1abc9c, "vending_new": 0x2ecc71, "vending_watch": 0x2ecc71, "vending_tracked": 0x2ecc71,
}

func sendDiscord(webhook, title, text, kind, server string) error {
	color, ok := kindColors[kind]
	if !ok {
		color = 0xce422b
	}
	payload := map[string]any{
		"username": "RustWatch",
		"embeds": []map[string]any{{
			"title": title, "description": text, "color": color,
			"footer":    map[string]string{"text": server},
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		}},
	}
	b, _ := json.Marshal(payload)
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Post(webhook, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
