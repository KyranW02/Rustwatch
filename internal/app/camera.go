package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"sort"
	"strings"
	"sync"
	"time"

	"rustwatch/internal/rustplus"
)

// Base cameras through Rust+: CCTV, PTZ cameras, auto turrets and drones that were given an ID
// at a computer station. Rust+ only streams them while you are NOT on the server, so this is
// for checking on the base from outside the game. One camera is open at a time, kept alive
// while the Cameras page is looking at it.

type CameraConfig struct {
	ID   string `json:"id"`   // the identifier set at the computer station (case-sensitive)
	Name string `json:"name"` // friendly name
}

type Cameras struct {
	a  *App
	mu sync.Mutex

	id       string
	info     *rustplus.CameraInfo
	order    []int16
	pix      []rustplus.Sample
	frames   int
	lastRays time.Time
	lastView time.Time
	opened   time.Time
	err      string
	players  map[string]time.Time
	cancel   context.CancelFunc
	pngCache []byte
	pngAt    int
}

func newCameras(a *App) *Cameras { return &Cameras{a: a, players: map[string]time.Time{}} }

type CameraView struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Width    int32    `json:"width"`
	Height   int32    `json:"height"`
	Controls int32    `json:"controls"`
	Frames   int      `json:"frames"`
	Error    string   `json:"error"`
	Players  []string `json:"players"`
	Live     bool     `json:"live"`
	Since    int64    `json:"since"`
}

type CamerasView struct {
	List   []CameraConfig `json:"list"`
	Active *CameraView    `json:"active"`
}

// viewLocked is called with a.mu held.
func (c *Cameras) viewLocked(list []CameraConfig) CamerasView {
	v := CamerasView{List: list}
	if v.List == nil {
		v.List = []CameraConfig{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id == "" {
		return v
	}
	cv := &CameraView{ID: c.id, Frames: c.frames, Error: c.err, Live: time.Since(c.lastRays) < 5*time.Second, Since: c.opened.Unix()}
	for _, x := range list {
		if x.ID == c.id {
			cv.Name = x.Name
		}
	}
	if c.info != nil {
		cv.Width, cv.Height, cv.Controls = c.info.Width, c.info.Height, c.info.ControlFlags
	}
	for n, t := range c.players {
		if time.Since(t) < 10*time.Second {
			cv.Players = append(cv.Players, n)
		}
	}
	sort.Strings(cv.Players)
	v.Active = cv
	return v
}

// Open starts streaming a camera, replacing any open one.
func (c *Cameras) Open(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("enter the camera ID")
	}
	c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.id, c.info, c.order, c.pix, c.frames, c.err = id, nil, nil, nil, 0, ""
	c.lastView, c.opened, c.lastRays = time.Now(), time.Now(), time.Time{}
	c.players = map[string]time.Time{}
	c.cancel = cancel
	c.pngCache = nil
	c.mu.Unlock()
	go c.keepAlive(ctx, id)
	return nil
}

// Close stops the open camera.
func (c *Cameras) Close() {
	c.mu.Lock()
	cancel, had := c.cancel, c.id != ""
	c.cancel, c.id = nil, ""
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if had {
		if cl := c.client(); cl != nil {
			ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
			cl.CameraUnsubscribe(ctx)
			done()
		}
	}
}

func (c *Cameras) client() *rustplus.Client {
	c.a.mu.Lock()
	defer c.a.mu.Unlock()
	return c.a.client
}

// Touch marks the camera as still being watched.
func (c *Cameras) Touch() {
	c.mu.Lock()
	c.lastView = time.Now()
	c.mu.Unlock()
}

func cameraHint(err error) string {
	m := err.Error()
	low := strings.ToLower(m)
	switch {
	case strings.Contains(low, "not connected"):
		return "RustWatch isn't connected to the server."
	case strings.Contains(low, "online") || strings.Contains(low, "in game") || strings.Contains(low, "ingame"):
		return "Rust+ only shows cameras while you're off the server. Leave the server, then open the camera again. (" + m + ")"
	case strings.Contains(low, "not_found") || strings.Contains(low, "no_camera") || strings.Contains(low, "not found"):
		return "No camera with that ID. IDs are case-sensitive and set at a computer station; a destroyed camera can't be opened. (" + m + ")"
	}
	return "Couldn't open the camera: " + m + ". Check the ID (it's case-sensitive) and that you're not on the server: Rust+ only streams cameras while you're offline."
}

// keepAlive subscribes and renews every 10 s until closed or nobody has looked for a minute.
func (c *Cameras) keepAlive(ctx context.Context, id string) {
	for {
		c.mu.Lock()
		idle := time.Since(c.lastView) > time.Minute
		c.mu.Unlock()
		if idle {
			c.Close()
			return
		}
		cl := c.client()
		var err error
		var info *rustplus.CameraInfo
		if cl == nil {
			err = rustplus.ErrNotConnected
		} else {
			rctx, done := context.WithTimeout(ctx, 15*time.Second)
			info, err = cl.CameraSubscribe(rctx, id)
			done()
		}
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		if c.id != id {
			c.mu.Unlock()
			return
		}
		if err != nil {
			c.err = cameraHint(err)
		} else {
			c.err = ""
			if c.info == nil || c.info.Width != info.Width || c.info.Height != info.Height {
				w, h := int(info.Width), int(info.Height)
				if w > 0 && h > 0 && w*h <= 1<<20 {
					c.order = rustplus.SampleOrder(w, h)
					c.pix = make([]rustplus.Sample, w*h)
				}
			}
			c.info = info
		}
		c.mu.Unlock()
		c.a.publish("update", nil)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// onRays decodes a slice of the camera image.
func (c *Cameras) onRays(r *rustplus.CameraRays) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id == "" || c.info == nil || c.pix == nil {
		return
	}
	rustplus.DecodeRays(r, c.order, int(c.info.Width), int(c.info.Height), c.pix)
	c.frames++
	c.lastRays = time.Now()
	for _, e := range r.Entities {
		if e.Type == 2 && e.Name != "" {
			c.players[e.Name] = time.Now()
		}
	}
}

// PNG renders the current camera picture.
func (c *Cameras) PNG() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id == "" || c.info == nil || c.pix == nil {
		return nil, errors.New("no camera picture yet")
	}
	c.lastView = time.Now()
	if c.pngCache != nil && c.pngAt == c.frames {
		return c.pngCache, nil
	}
	img := rustplus.RenderFrame(c.pix, int(c.info.Width), int(c.info.Height))
	var b bytes.Buffer
	if err := png.Encode(&b, upscale(img, 2)); err != nil {
		return nil, err
	}
	c.pngCache, c.pngAt = b.Bytes(), c.frames
	return c.pngCache, nil
}

func upscale(src *image.RGBA, k int) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx()*k, b.Dy()*k))
	for y := 0; y < b.Dy()*k; y++ {
		for x := 0; x < b.Dx()*k; x++ {
			dst.SetRGBA(x, y, src.RGBAAt(x/k, y/k))
		}
	}
	return dst
}

// Input sends held buttons and mouse movement to the open camera.
func (c *Cameras) Input(buttons int32, dx, dy float32) error {
	c.mu.Lock()
	open := c.id != ""
	c.lastView = time.Now()
	c.mu.Unlock()
	if !open {
		return errors.New("no camera open")
	}
	cl := c.client()
	if cl == nil {
		return rustplus.ErrNotConnected
	}
	ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	return cl.CameraInput(ctx, buttons, dx, dy)
}

// ---- saved camera list ----

func (a *App) SaveCamera(id, name string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("enter the camera ID set at the computer station")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.srv == nil {
		return errors.New("connect to a server first")
	}
	if name = strings.TrimSpace(name); name == "" {
		name = id
	}
	for i := range a.srv.Cameras {
		if a.srv.Cameras[i].ID == id {
			a.srv.Cameras[i].Name = name
			a.saveLocked()
			return nil
		}
	}
	if len(a.srv.Cameras) >= 50 {
		return fmt.Errorf("that's a lot of cameras: remove some first")
	}
	a.srv.Cameras = append(a.srv.Cameras, CameraConfig{ID: id, Name: name})
	a.saveLocked()
	return nil
}

func (a *App) RemoveCamera(id string) {
	a.mu.Lock()
	if a.srv != nil {
		out := a.srv.Cameras[:0]
		for _, x := range a.srv.Cameras {
			if x.ID != id {
				out = append(out, x)
			}
		}
		a.srv.Cameras = out
		a.saveLocked()
	}
	a.mu.Unlock()
	a.cams.mu.Lock()
	open := a.cams.id == id
	a.cams.mu.Unlock()
	if open {
		a.cams.Close()
	}
}
