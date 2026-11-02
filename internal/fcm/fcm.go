// Package fcm registers this app as a "phone" with Google FCM + Expo + Facepunch so that
// Rust+ pairing requests (servers, smart switches, alarms, storage monitors) and alarm
// notifications are pushed to the app, then listens for them over Google's MCS protocol.
package fcm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"rustwatch/internal/pb"
)

// Rust+ companion app (Android) Firebase constants.
const (
	apiKey         = "AIzaSyB5y2y-Tzqb4-I4Qnlsh_9naYv_TD8pCvY"
	projectID      = "rust-companion-app"
	gcmSenderID    = "976529667804"
	gmsAppID       = "1:976529667804:android:d6f1ddeb4403b338fea619"
	androidPackage = "com.facepunch.rust.companion"
	androidCert    = "E28D05345FB78A7A1A63D70F4A302DBF426CA5AD"
	firebaseClient = "android-min-sdk/23 fire-core/20.0.0 device-name/a21snnxx device-brand/samsung device-model/a21s android-installer/com.android.vending fire-android/30 fire-installations/17.0.0 fire-fcm/22.0.0 android-platform/ kotlin/1.9.23 android-target-sdk/34"
)

// Credentials are everything needed to keep listening for pushes.
type Credentials struct {
	AndroidID     string `json:"androidId"`
	SecurityToken string `json:"securityToken"`
	FCMToken      string `json:"fcmToken"`
	ExpoToken     string `json:"expoToken"`
	AuthToken     string `json:"rustplusAuthToken"`
	SteamID       string `json:"steamId"`
}

var httpc = &http.Client{Timeout: 30 * time.Second}

// RegisterDevice creates a Firebase installation, checks in with GCM and registers for FCM,
// then fetches an Expo push token. Returns credentials without the Rust+ auth token.
func RegisterDevice(ctx context.Context) (*Credentials, error) {
	fisToken, err := installRequest(ctx)
	if err != nil {
		return nil, fmt.Errorf("firebase install: %w", err)
	}
	aid, sec, err := Checkin(ctx, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("gcm checkin: %w", err)
	}
	var token string
	for attempt := 0; attempt < 6; attempt++ {
		token, err = registerRequest(ctx, aid, sec, fisToken)
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("gcm register: %w", err)
	}
	expo, err := expoToken(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("expo token: %w", err)
	}
	return &Credentials{
		AndroidID:     strconv.FormatUint(aid, 10),
		SecurityToken: strconv.FormatUint(sec, 10),
		FCMToken:      token,
		ExpoToken:     expo,
	}, nil
}

func fid() string {
	b := make([]byte, 17)
	rand.Read(b)
	b[0] = 0x70 | (b[0] & 0x0f)
	return strings.TrimRight(base64.StdEncoding.EncodeToString(b), "=")
}

func installRequest(ctx context.Context) (string, error) {
	body, _ := json.Marshal(map[string]string{"fid": fid(), "appId": gmsAppID, "authVersion": "FIS_v2", "sdkVersion": "a:17.0.0"})
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://firebaseinstallations.googleapis.com/v1/projects/"+projectID+"/installations", bytes.NewReader(body))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Android-Package", androidPackage)
	req.Header.Set("X-Android-Cert", androidCert)
	req.Header.Set("x-firebase-client", firebaseClient)
	req.Header.Set("x-firebase-client-log-type", "3")
	req.Header.Set("x-goog-api-key", apiKey)
	req.Header.Set("User-Agent", "Dalvik/2.1.0 (Linux; U; Android 11; SM-A217F Build/RP1A.200720.012)")
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		AuthToken struct {
			Token string `json:"token"`
		} `json:"authToken"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &out); err != nil || out.AuthToken.Token == "" {
		return "", fmt.Errorf("unexpected response %d: %.200s", resp.StatusCode, raw)
	}
	return out.AuthToken.Token, nil
}

// Checkin performs an Android GCM checkin. Pass zeros for a new device.
func Checkin(ctx context.Context, androidID, securityToken uint64) (uint64, uint64, error) {
	chrome := &pb.Writer{}
	chrome.Uint(1, 2) // platform
	chrome.String(2, "63.0.3234.0")
	chrome.Uint(3, 1) // channel stable
	checkin := &pb.Writer{}
	checkin.Uint(12, 3) // DEVICE_CHROME_BROWSER
	checkin.Message(13, chrome)
	w := &pb.Writer{}
	if androidID != 0 {
		w.Uint(2, androidID)
	}
	w.Message(4, checkin)
	if securityToken != 0 {
		w.Fixed64(13, securityToken)
	}
	w.Uint(14, 3)
	w.Uint(22, 0)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://android.clients.google.com/checkin", bytes.NewReader(w.Bytes()))
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := httpc.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return 0, 0, fmt.Errorf("checkin status %d", resp.StatusCode)
	}
	var aid, sec uint64
	pb.Each(raw, func(f pb.Field) {
		switch f.Num {
		case 7:
			aid = f.V
		case 8:
			sec = f.V
		}
	})
	if aid == 0 || sec == 0 {
		return 0, 0, errors.New("checkin response missing ids")
	}
	return aid, sec, nil
}

func registerRequest(ctx context.Context, aid, sec uint64, fisToken string) (string, error) {
	form := url.Values{
		"device":                             {strconv.FormatUint(aid, 10)},
		"app":                                {androidPackage},
		"cert":                               {androidCert},
		"app_ver":                            {"1"},
		"X-subtype":                          {gcmSenderID},
		"X-app_ver":                          {"1"},
		"X-osv":                              {"29"},
		"X-cliv":                             {"fiid-21.1.1"},
		"X-gmsv":                             {"220217001"},
		"X-scope":                            {"*"},
		"X-Goog-Firebase-Installations-Auth": {fisToken},
		"X-gms_app_id":                       {gmsAppID},
		"X-Firebase-Client":                  {firebaseClient},
		"X-Firebase-Client-Log-Type":         {"1"},
		"X-app_ver_name":                     {"1"},
		"target_ver":                         {"31"},
		"sender":                             {gcmSenderID},
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://android.clients.google.com/c2dm/register3", strings.NewReader(form.Encode()))
	req.Header.Set("Authorization", fmt.Sprintf("AidLogin %d:%d", aid, sec))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	s := string(raw)
	if strings.Contains(s, "Error") || !strings.Contains(s, "=") {
		return "", fmt.Errorf("register failed: %.200s", s)
	}
	return strings.TrimSpace(strings.SplitN(s, "=", 2)[1]), nil
}

func uuid4() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func expoToken(ctx context.Context, fcmToken string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"type": "fcm", "deviceId": uuid4(), "development": false,
		"appId": androidPackage, "deviceToken": fcmToken,
		"projectId": "49451aca-a822-41e6-ad59-955718d0ff9c",
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://exp.host/--/api/v2/push/getExpoPushToken", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Data struct {
			ExpoPushToken string `json:"expoPushToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Data.ExpoPushToken == "" {
		return "", fmt.Errorf("unexpected response %d: %.200s", resp.StatusCode, raw)
	}
	return out.Data.ExpoPushToken, nil
}

// RegisterWithRustPlus tells Facepunch to send this account's Rust+ pushes to our Expo token.
func RegisterWithRustPlus(ctx context.Context, authToken, expoPushToken string) error {
	body, _ := json.Marshal(map[string]any{
		"AuthToken": authToken, "DeviceId": "rustwatch", "PushKind": 3, "PushToken": expoPushToken,
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://companion-rust.facepunch.com:443/api/push/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("facepunch register %d: %.200s", resp.StatusCode, raw)
	}
	return nil
}

// ---------------- MCS listener ----------------

const (
	mcsVersion         = 41
	tagHeartbeatPing   = 0
	tagHeartbeatAck    = 1
	tagLoginRequest    = 2
	tagLoginResponse   = 3
	tagClose           = 4
	tagIqStanza        = 7
	tagDataMessage     = 8
	tagStreamErrStanza = 10
)

// Push is one received data message.
type Push struct {
	PersistentID string
	Data         map[string]string
}

// Listener keeps a connection to mtalk.google.com and delivers pushes.
type Listener struct {
	AndroidID     uint64
	SecurityToken uint64
	OnPush        func(Push)
	OnStatus      func(connected bool, err error)
	seen          map[string]bool
	pendingAck    []string
}

func (l *Listener) Run(ctx context.Context) {
	l.seen = map[string]bool{}
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := l.session(ctx)
		if l.OnStatus != nil {
			l.OnStatus(false, err)
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 60*time.Second {
			backoff *= 2
		}
	}
}

func loginRequest(aid, sec uint64, persistent []string) []byte {
	w := &pb.Writer{}
	w.String(1, "chrome-63.0.3234.0")
	w.String(2, "mcs.android.com")
	w.String(3, strconv.FormatUint(aid, 10))
	w.String(4, strconv.FormatUint(aid, 10))
	w.String(5, strconv.FormatUint(sec, 10))
	w.String(6, "android-"+strconv.FormatUint(aid, 16))
	set := &pb.Writer{}
	set.String(1, "new_vc")
	set.String(2, "1")
	w.Message(8, set)
	for _, id := range persistent {
		w.String(10, id)
	}
	w.Bool(12, false)
	w.Bool(14, true)
	w.Uint(16, 2)
	w.Uint(17, 1)
	return w.Bytes()
}

func frame(tag byte, msg []byte) []byte {
	b := []byte{tag}
	b = pb.AppendVarint(b, uint64(len(msg)))
	return append(b, msg...)
}

func (l *Listener) session(ctx context.Context) error {
	if _, _, err := Checkin(ctx, l.AndroidID, l.SecurityToken); err != nil {
		return fmt.Errorf("checkin: %w", err)
	}
	d := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", "mtalk.google.com:5228", &tls.Config{ServerName: "mtalk.google.com"})
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	login := append([]byte{mcsVersion}, frame(tagLoginRequest, loginRequest(l.AndroidID, l.SecurityToken, l.pendingAck))...)
	if _, err := conn.Write(login); err != nil {
		return err
	}
	// keep-alive pings
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(4 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				conn.Write(frame(tagHeartbeatPing, nil))
			}
		}
	}()

	r := &mcsReader{conn: conn}
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	ver, err := r.byte()
	if err != nil {
		return err
	}
	if ver < mcsVersion && ver != 38 {
		return fmt.Errorf("unexpected MCS version %d", ver)
	}
	for {
		conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
		tag, msg, err := r.message()
		if err != nil {
			return err
		}
		switch tag {
		case tagHeartbeatPing:
			conn.Write(frame(tagHeartbeatAck, nil))
		case tagLoginResponse:
			var errInfo string
			pb.Each(msg, func(f pb.Field) {
				if f.Num == 3 {
					errInfo = "login error"
				}
			})
			if errInfo != "" {
				return errors.New(errInfo)
			}
			l.pendingAck = nil
			if l.OnStatus != nil {
				l.OnStatus(true, nil)
			}
		case tagClose:
			return errors.New("server closed connection")
		case tagDataMessage:
			p := ParseDataMessage(msg)
			if p.PersistentID != "" {
				if l.seen[p.PersistentID] {
					continue
				}
				l.seen[p.PersistentID] = true
				l.pendingAck = append(l.pendingAck, p.PersistentID)
			}
			if l.OnPush != nil {
				l.OnPush(p)
			}
		}
	}
}

type mcsReader struct {
	conn net.Conn
	buf  []byte
}

func (r *mcsReader) fill(n int) error {
	for len(r.buf) < n {
		tmp := make([]byte, 32<<10)
		k, err := r.conn.Read(tmp)
		r.buf = append(r.buf, tmp[:k]...)
		if err != nil && len(r.buf) < n {
			return err
		}
	}
	return nil
}

func (r *mcsReader) byte() (byte, error) {
	if err := r.fill(1); err != nil {
		return 0, err
	}
	b := r.buf[0]
	r.buf = r.buf[1:]
	return b, nil
}

func (r *mcsReader) message() (byte, []byte, error) {
	tag, err := r.byte()
	if err != nil {
		return 0, nil, err
	}
	var size uint64
	for i := 1; ; i++ {
		if err := r.fill(i); err != nil {
			return 0, nil, err
		}
		v, n := pb.ReadVarint(r.buf)
		if n > 0 {
			size = v
			r.buf = r.buf[n:]
			break
		}
		if i > 5 {
			return 0, nil, errors.New("bad MCS size")
		}
	}
	if size > 16<<20 {
		return 0, nil, errors.New("MCS message too large")
	}
	if err := r.fill(int(size)); err != nil {
		return 0, nil, err
	}
	msg := r.buf[:size]
	r.buf = r.buf[size:]
	return tag, append([]byte(nil), msg...), nil
}

// ParseUint64 helps convert stored string ids.
func ParseUint64(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}

// ParseDataMessage decodes a DataMessageStanza.
func ParseDataMessage(msg []byte) Push {
	p := Push{Data: map[string]string{}}
	pb.Each(msg, func(f pb.Field) {
		switch f.Num {
		case 9:
			p.PersistentID = f.Str()
		case 7:
			var k, v string
			pb.Each(f.B, func(g pb.Field) {
				if g.Num == 1 {
					k = g.Str()
				} else if g.Num == 2 {
					v = g.Str()
				}
			})
			p.Data[k] = v
		}
	})
	return p
}
