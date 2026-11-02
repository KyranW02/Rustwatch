package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Two-way Discord: with a bot token and a channel, teammates can run RustWatch commands from
// Discord (!pop, !raid, !turrets on…), team chat can be copied into the channel, and Discord
// messages can be posted into team chat. Alerts still go through the webhook.

type DiscordBot struct {
	a    *App
	http *http.Client
	base string

	mu      sync.Mutex
	state   string
	botName string
	botID   string
	lastID  string
	key     string // token+channel the state belongs to
	out     chan string
}

func newDiscordBot(a *App) *DiscordBot {
	return &DiscordBot{a: a, http: &http.Client{Timeout: 20 * time.Second}, base: "https://discord.com/api/v10", out: make(chan string, 50), state: "off"}
}

type discordMsg struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Author  struct {
		ID         string `json:"id"`
		Username   string `json:"username"`
		GlobalName string `json:"global_name"`
		Bot        bool   `json:"bot"`
	} `json:"author"`
	WebhookID string `json:"webhook_id"`
}

func (d *DiscordBot) setState(s string) {
	d.mu.Lock()
	changed := d.state != s
	d.state = s
	d.mu.Unlock()
	if changed {
		d.a.publish("update", nil)
	}
}

func (d *DiscordBot) Status() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

func (d *DiscordBot) call(ctx context.Context, method, path, token string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	for try := 0; try < 3; try++ {
		req, err := http.NewRequestWithContext(ctx, method, d.base+path, rd)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bot "+token)
		req.Header.Set("User-Agent", "DiscordBot (https://github.com/KyranW02/Rustwatch, "+Version+")")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := d.http.Do(req)
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == 429 {
			var rl struct {
				RetryAfter float64 `json:"retry_after"`
			}
			json.Unmarshal(data, &rl)
			wait := time.Duration(max(rl.RetryAfter, 1) * float64(time.Second))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(min(wait, 30*time.Second)):
			}
			if body != nil {
				b, _ := json.Marshal(body)
				rd = bytes.NewReader(b)
			}
			continue
		}
		switch resp.StatusCode {
		case 401:
			return errors.New("Discord rejected the bot token")
		case 403:
			return errors.New("the bot can't see that channel: invite it to the server and give it View Channel, Read Message History and Send Messages")
		case 404:
			return errors.New("channel not found: check the channel ID")
		}
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("Discord error %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		if out != nil {
			return json.Unmarshal(data, out)
		}
		return nil
	}
	return errors.New("Discord kept rate-limiting the bot")
}

// Relay queues a line for the Discord channel (team chat copy).
func (d *DiscordBot) Relay(text string) {
	select {
	case d.out <- text:
	default:
	}
}

func (d *DiscordBot) run() {
	for {
		d.a.mu.Lock()
		st := d.a.cfg.Settings
		d.a.mu.Unlock()
		token, ch := strings.TrimSpace(st.DiscordBotToken), strings.TrimSpace(st.DiscordChannel)
		if token == "" || ch == "" {
			d.setState("off")
			d.drain()
			time.Sleep(3 * time.Second)
			continue
		}
		if err := d.session(token, ch); err != nil {
			d.setState("error: " + err.Error())
			time.Sleep(20 * time.Second)
		}
	}
}

func (d *DiscordBot) drain() {
	for {
		select {
		case <-d.out:
		default:
			return
		}
	}
}

// session runs until the settings change or an error happens.
func (d *DiscordBot) session(token, ch string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var me struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err := d.call(ctx, "GET", "/users/@me", token, nil, &me); err != nil {
		return err
	}
	d.mu.Lock()
	if d.key != token+"|"+ch {
		d.key, d.lastID = token+"|"+ch, ""
	}
	d.botName, d.botID = me.Username, me.ID
	d.mu.Unlock()
	// start after the newest message, so old ones aren't replayed
	if d.lastID == "" {
		var latest []discordMsg
		if err := d.call(ctx, "GET", "/channels/"+ch+"/messages?limit=1", token, nil, &latest); err != nil {
			return err
		}
		d.lastID = "0"
		if len(latest) > 0 {
			d.lastID = latest[0].ID
		}
	}
	d.setState("connected as " + me.Username)
	poll := time.NewTicker(4 * time.Second)
	defer poll.Stop()
	for {
		d.a.mu.Lock()
		st := d.a.cfg.Settings
		d.a.mu.Unlock()
		if strings.TrimSpace(st.DiscordBotToken) != token || strings.TrimSpace(st.DiscordChannel) != ch {
			return nil
		}
		select {
		case text := <-d.out:
			if st.DiscordRelayChat {
				if err := d.send(ctx, token, ch, text); err != nil {
					return err
				}
			}
			continue
		case <-poll.C:
		}
		var msgs []discordMsg
		if err := d.call(ctx, "GET", "/channels/"+ch+"/messages?limit=50&after="+d.lastID, token, nil, &msgs); err != nil {
			return err
		}
		// Discord returns newest first
		for i := len(msgs) - 1; i >= 0; i-- {
			m := msgs[i]
			d.lastID = m.ID
			if m.Author.Bot || m.WebhookID != "" || m.Author.ID == me.ID {
				continue
			}
			d.handle(ctx, token, ch, m, st)
		}
	}
}

func (d *DiscordBot) send(ctx context.Context, token, ch, text string) error {
	if len(text) > 1900 {
		text = text[:1900] + "…"
	}
	return d.call(ctx, "POST", "/channels/"+ch+"/messages", token, map[string]any{"content": text, "allowed_mentions": map[string]any{"parse": []string{}}}, nil)
}

func (d *DiscordBot) handle(ctx context.Context, token, ch string, m discordMsg, st Settings) {
	text := strings.TrimSpace(m.Content)
	if text == "" {
		return
	}
	name := m.Author.GlobalName
	if name == "" {
		name = m.Author.Username
	}
	if strings.HasPrefix(text, st.Prefix) {
		line := strings.TrimPrefix(text, st.Prefix)
		reply := d.a.Command(0, line)
		if reply == "" {
			reply = "Unknown command. Try " + st.Prefix + "help"
		}
		d.send(ctx, token, ch, reply)
		return
	}
	if st.DiscordToTeam {
		if err := d.a.SendChat(fmt.Sprintf("[Discord] %s: %s", name, text)); err != nil {
			d.send(ctx, token, ch, "Couldn't post that to team chat: "+err.Error())
		}
	}
}
