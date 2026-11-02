package app

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rustwatch/internal/sound"
)

// Rust's Aug 2026 update stopped sending event markers to Rust+, so RustWatch listens to the
// game instead: it records Rust's own audio, keeps the last 30 seconds in memory, and
// recognises cue sounds the player has taught it (see internal/sound). Nothing is written to
// disk except the clips the player saves as cues.

// soundEvents are the cues RustWatch knows what to do with.
var soundEvents = []struct {
	Key, Label, Hint string
	Cooldown         time.Duration
}{
	{"cargo", "Cargo ship", "The horn when the cargo ship comes in.", 20 * time.Minute},
	{"oil_small", "Small Oil Rig called", "The sound when someone triggers Small Oil Rig and heavy scientists are called in.", 10 * time.Minute},
	{"oil_large", "Large Oil Rig called", "The sound when someone triggers Large Oil Rig and heavy scientists are called in.", 10 * time.Minute},
	{"excavator", "Giant excavator started", "The sound when someone starts the giant excavator.", 10 * time.Minute},
	{"deepsea", "Deep Sea", "The cue when Deep Sea opens.", 30 * time.Minute},
	{"bradley", "Bradley destroyed", "The explosion when Bradley is destroyed. Only heard nearby.", 10 * time.Minute},
	{"heli_down", "Patrol heli shot down", "The crash when the patrol heli goes down. Only heard nearby.", 10 * time.Minute},
	{"vendor", "Travelling vendor", "The travelling vendor's horn. Only heard nearby.", 10 * time.Minute},
}

func soundEventLabel(k string) string {
	for _, e := range soundEvents {
		if e.Key == k {
			return e.Label
		}
	}
	return k
}

// audioSource records game audio and hands it over as mono samples at sound.Rate.
type audioSource interface {
	Ready() (bool, string)
	Run(stop <-chan struct{}, feed func([]float32), started func(mode string)) error
}

const ringSeconds = 30

type cue struct {
	ID      string  `json:"id"` // file name without .wav
	Event   string  `json:"event"`
	Label   string  `json:"label"`
	Seconds float64 `json:"seconds"`
	Created int64   `json:"created"`
	fp      sound.Fingerprint
}

type heardEntry struct {
	Time  int64   `json:"time"`
	Event string  `json:"event"`
	Label string  `json:"label"`
	Match float64 `json:"match"` // share of the cue's sound found
}

type pendingClip struct {
	samples []float32
	at      time.Time
}

// SoundWatch is the sound-detection service.
type SoundWatch struct {
	a   *App
	src audioSource
	dir string

	mu       sync.Mutex
	ring     []float32
	pos      int
	filled   bool
	state    string // what the UI shows
	mode     string // "process", "system", "demo", ""
	level    float64
	cues     []cue
	matcher  *sound.Matcher
	maxCue   int // longest cue in samples
	cool     map[string]time.Time
	heard    []heardEntry
	clips    map[string]pendingClip
	lastBest map[string]float64 // best recent score per event, for the UI
}

func newSoundWatch(a *App, dir string, src audioSource) *SoundWatch {
	w := &SoundWatch{a: a, src: src, dir: filepath.Join(dir, "sounds"), ring: make([]float32, sound.Rate*ringSeconds),
		cool: map[string]time.Time{}, clips: map[string]pendingClip{}, lastBest: map[string]float64{}, state: "starting"}
	w.loadCues()
	return w
}

func (w *SoundWatch) enabled() bool {
	w.a.mu.Lock()
	defer w.a.mu.Unlock()
	return !w.a.cfg.Settings.SoundOff
}

func (w *SoundWatch) setState(s, mode string) {
	w.mu.Lock()
	changed := w.state != s
	w.state, w.mode = s, mode
	w.mu.Unlock()
	if changed {
		log.Println("sound:", s)
		w.a.publish("update", nil)
	}
}

// Start runs capture and detection in the background.
func (w *SoundWatch) Start() {
	go w.captureLoop()
	go w.detectLoop()
}

func (w *SoundWatch) captureLoop() {
	if w.src == nil {
		w.setState("Sound detection works on Windows only.", "")
		return
	}
	for {
		if !w.enabled() {
			w.setState("Off. Turn it on in Settings → Sound detection.", "")
			time.Sleep(3 * time.Second)
			continue
		}
		if ok, note := w.src.Ready(); !ok {
			w.setState(note, "")
			time.Sleep(3 * time.Second)
			continue
		}
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() { // stop when Rust closes or detection is switched off
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					if ok, _ := w.src.Ready(); !ok || !w.enabled() {
						close(stop)
						return
					}
				}
			}
		}()
		err := w.src.Run(stop, w.feed, func(mode string) {
			switch mode {
			case "process":
				w.setState("Listening to Rust's audio only.", mode)
			case "system":
				w.setState("Listening to all PC audio. This Windows version can't record Rust on its own, so keep other sounds quiet.", mode)
			default:
				w.setState("Listening ("+mode+").", mode)
			}
		})
		close(done)
		if err != nil {
			w.setState("Couldn't record game audio: "+err.Error(), "")
			time.Sleep(15 * time.Second)
		}
	}
}

func (w *SoundWatch) feed(mono []float32) {
	w.mu.Lock()
	for _, v := range mono {
		w.ring[w.pos] = v
		w.pos++
		if w.pos == len(w.ring) {
			w.pos, w.filled = 0, true
		}
	}
	w.mu.Unlock()
}

// last returns the most recent sec seconds, oldest first.
func (w *SoundWatch) last(sec float64) []float32 {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := int(sec * sound.Rate)
	have := w.pos
	if w.filled {
		have = len(w.ring)
	}
	if n > have {
		n = have
	}
	out := make([]float32, n)
	start := w.pos - n
	for i := 0; i < n; i++ {
		j := start + i
		if j < 0 {
			j += len(w.ring)
		}
		out[i] = w.ring[j]
	}
	return out
}

func (w *SoundWatch) detectLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		lv := sound.RMS(w.last(0.5))
		w.mu.Lock()
		w.level = lv
		m, maxCue, mode := w.matcher, w.maxCue, w.mode
		w.mu.Unlock()
		if m == nil || mode == "" {
			continue
		}
		win := w.last(float64(maxCue)/sound.Rate + 5)
		if len(win) < sound.Rate*2 {
			continue
		}
		w.check(m, win)
	}
}

// check runs detection on a window of audio.
func (w *SoundWatch) check(m *sound.Matcher, win []float32) []string {
	var found []string
	scores := m.Match(sound.Analyze(win))
	now := time.Now()
	w.mu.Lock()
	for k := range w.lastBest {
		w.lastBest[k] *= 0.9
	}
	type hit struct {
		ev    string
		ratio float64
	}
	var hits []hit
	for _, s := range scores {
		ev := strings.SplitN(m.Refs[s.Ref].ID, "-", 2)[0]
		if s.Excess > w.lastBest[ev] {
			w.lastBest[ev] = s.Excess
		}
		if !s.Detected() {
			continue
		}
		cd := 10 * time.Minute
		for _, e := range soundEvents {
			if e.Key == ev {
				cd = e.Cooldown
			}
		}
		if now.Sub(w.cool[ev]) < cd {
			continue
		}
		w.cool[ev] = now
		hits = append(hits, hit{ev, s.Ratio})
		w.heard = append(w.heard, heardEntry{Time: now.Unix(), Event: ev, Label: soundEventLabel(ev), Match: math.Round(s.Ratio*100) / 100})
		if len(w.heard) > 100 {
			w.heard = w.heard[len(w.heard)-100:]
		}
	}
	w.mu.Unlock()
	for _, h := range hits {
		log.Printf("sound: heard %s (%.0f%% of the cue)", h.ev, h.ratio*100)
		found = append(found, h.ev)
		w.a.mu.Lock()
		if w.a.events != nil {
			w.a.events.Heard(h.ev)
		} else {
			w.a.alertLockedForce(soundEventLabel(h.ev)+" heard", "Heard "+soundEventLabel(h.ev)+" in game")
		}
		w.a.mu.Unlock()
	}
	if len(hits) > 0 {
		w.a.publish("update", nil)
	}
	return found
}

// ---- saved cues ----

func (w *SoundWatch) loadCues() {
	os.MkdirAll(w.dir, 0o755)
	ents, _ := os.ReadDir(w.dir)
	var cues []cue
	var refs []sound.Ref
	maxCue := 0
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".wav") {
			continue
		}
		id := strings.TrimSuffix(name, filepath.Ext(name))
		ev, ts, _ := strings.Cut(id, "-")
		b, err := os.ReadFile(filepath.Join(w.dir, name))
		if err != nil {
			continue
		}
		x, err := sound.DecodeWAV(b)
		if err != nil {
			log.Printf("sound: skipping %s: %v", name, err)
			continue
		}
		c := cue{ID: id, Event: ev, Label: soundEventLabel(ev), Seconds: math.Round(float64(len(x))/sound.Rate*10) / 10, fp: sound.Analyze(x)}
		c.Created, _ = strconv.ParseInt(ts, 10, 64)
		cues = append(cues, c)
		refs = append(refs, sound.Ref{ID: id, FP: c.fp})
		maxCue = max(maxCue, len(x))
	}
	sort.Slice(cues, func(i, j int) bool { return cues[i].Created > cues[j].Created })
	w.mu.Lock()
	w.cues, w.maxCue = cues, maxCue
	w.matcher = nil
	if len(refs) > 0 {
		w.matcher = sound.NewMatcher(refs)
	}
	w.mu.Unlock()
}

// Capture keeps the last 30 s of game audio as a clip the player can cut a cue from.
func (w *SoundWatch) Capture() (string, []float32, error) {
	x := w.last(ringSeconds)
	if len(x) < sound.Rate {
		return "", nil, errors.New("nothing recorded yet — RustWatch needs Rust running and sound detection on")
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	w.mu.Lock()
	w.clips[id] = pendingClip{samples: x, at: time.Now()}
	for k, c := range w.clips { // keep a few
		if time.Since(c.at) > 30*time.Minute {
			delete(w.clips, k)
		}
	}
	w.mu.Unlock()
	return id, x, nil
}

// Import opens a sound file (e.g. one extracted from the game) as a clip to cut a cue from.
func (w *SoundWatch) Import(data []byte) (string, []float32, error) {
	x, err := sound.DecodeWAV(data)
	if err != nil {
		return "", nil, err
	}
	if len(x) < sound.Rate/4 {
		return "", nil, errors.New("that file is too short")
	}
	if len(x) > 120*sound.Rate {
		x = x[:120*sound.Rate] // only the first two minutes
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	w.mu.Lock()
	w.clips[id] = pendingClip{samples: x, at: time.Now()}
	w.mu.Unlock()
	return id, x, nil
}

func (w *SoundWatch) clip(id string, from, to float64) ([]float32, error) {
	w.mu.Lock()
	c, ok := w.clips[id]
	w.mu.Unlock()
	if !ok {
		return nil, errors.New("that recording has expired — capture it again")
	}
	a, b := int(from*sound.Rate), int(to*sound.Rate)
	if to <= 0 || b > len(c.samples) {
		b = len(c.samples)
	}
	if a < 0 {
		a = 0
	}
	if b-a < sound.Rate/4 {
		return nil, errors.New("select at least a quarter of a second")
	}
	return c.samples[a:b], nil
}

// SaveCue stores part of a captured clip as a cue for an event.
func (w *SoundWatch) SaveCue(clipID string, from, to float64, event string) (cue, error) {
	known := false
	for _, e := range soundEvents {
		known = known || e.Key == event
	}
	if !known {
		return cue{}, errors.New("unknown event")
	}
	x, err := w.clip(clipID, from, to)
	if err != nil {
		return cue{}, err
	}
	if len(x) > 12*sound.Rate {
		return cue{}, errors.New("keep a cue under 12 seconds — select just the sound itself")
	}
	fp := sound.Analyze(x)
	if fp.Peaks < 40 {
		return cue{}, errors.New("that selection is too quiet or plain to recognise — select the part where the sound is loudest")
	}
	id := fmt.Sprintf("%s-%d", event, time.Now().Unix())
	if err := os.WriteFile(filepath.Join(w.dir, id+".wav"), sound.EncodeWAV(x, sound.Rate), 0o644); err != nil {
		return cue{}, err
	}
	w.loadCues()
	w.a.publish("update", nil)
	return cue{ID: id, Event: event, Label: soundEventLabel(event), Seconds: float64(len(x)) / sound.Rate}, nil
}

// TestCue checks a selection against the whole captured clip and the other saved cues, so the
// player can see it would be recognised and wouldn't be confused with another event.
func (w *SoundWatch) TestCue(clipID string, from, to float64) (map[string]any, error) {
	x, err := w.clip(clipID, from, to)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	full := w.clips[clipID].samples
	cues := append([]cue(nil), w.cues...)
	w.mu.Unlock()
	self := sound.NewMatcher([]sound.Ref{{ID: "sel", FP: sound.Analyze(x)}})
	res := map[string]any{"peaks": sound.Analyze(x).Peaks}
	if sc := self.Match(sound.Analyze(full)); len(sc) > 0 {
		res["self"] = sc[0].Ratio
		res["selfOK"] = sc[0].Detected()
	}
	var confused []string
	if len(cues) > 0 {
		var refs []sound.Ref
		for _, c := range cues {
			refs = append(refs, sound.Ref{ID: c.ID, FP: c.fp})
		}
		for _, s := range sound.NewMatcher(refs).Match(sound.Analyze(x)) {
			if s.Detected() {
				confused = append(confused, cues[s.Ref].Label)
			}
		}
	}
	res["alreadyMatches"] = confused
	return res, nil
}

func (w *SoundWatch) DeleteCue(id string) error {
	if strings.ContainsAny(id, `/\.`) {
		return errors.New("bad id")
	}
	if err := os.Remove(filepath.Join(w.dir, id+".wav")); err != nil {
		return err
	}
	w.loadCues()
	w.a.publish("update", nil)
	return nil
}

func (w *SoundWatch) cueAudio(id string) ([]byte, error) {
	if strings.ContainsAny(id, `/\.`) {
		return nil, errors.New("bad id")
	}
	return os.ReadFile(filepath.Join(w.dir, id+".wav"))
}

// SoundView is what the UI shows.
type SoundView struct {
	State   string             `json:"state"`
	Mode    string             `json:"mode"`
	Level   float64            `json:"level"` // dBFS
	Cues    []cue              `json:"cues"`
	Heard   []heardEntry       `json:"heard"`
	Events  []map[string]any   `json:"events"`
	Closest map[string]float64 `json:"closest"`
	Dir     string             `json:"dir"`
}

func (w *SoundWatch) View() SoundView {
	w.mu.Lock()
	defer w.mu.Unlock()
	v := SoundView{State: w.state, Mode: w.mode, Cues: append([]cue{}, w.cues...), Dir: w.dir, Closest: map[string]float64{}}
	v.Level = -90
	if w.level > 0 {
		v.Level = math.Max(-90, 20*math.Log10(w.level))
	}
	for i := len(w.heard) - 1; i >= 0 && len(v.Heard) < 30; i-- {
		v.Heard = append(v.Heard, w.heard[i])
	}
	for k, s := range w.lastBest {
		v.Closest[k] = math.Round(s*100) / 100
	}
	for _, e := range soundEvents {
		n := 0
		for _, c := range w.cues {
			if c.Event == e.Key {
				n++
			}
		}
		v.Events = append(v.Events, map[string]any{"key": e.Key, "label": e.Label, "hint": e.Hint, "cues": n})
	}
	return v
}

// waveform returns a min/max envelope for drawing.
func waveform(x []float32, points int) [][2]float32 {
	out := make([][2]float32, points)
	per := max(1, len(x)/points)
	for i := 0; i < points; i++ {
		lo, hi := float32(0), float32(0)
		for j := i * per; j < (i+1)*per && j < len(x); j++ {
			lo, hi = min(lo, x[j]), max(hi, x[j])
		}
		out[i] = [2]float32{lo, hi}
	}
	return out
}

// spectrogramPNG draws a clip's spectrogram on a log frequency scale (~50 Hz–5.5 kHz, where
// game cues live), with an empty strip at the bottom for the page to draw the waveform in.
func spectrogramPNG(x []float32) []byte {
	spec := sound.Spectrogram(x)
	const h, strip = 160, 34
	wd := max(1, len(spec))
	img := image.NewNRGBA(image.Rect(0, 0, wd, h+strip))
	lo, hi := math.Log(4), math.Log(470)
	for t, fr := range spec {
		for y := 0; y < h; y++ {
			bin := int(math.Exp(lo + (hi-lo)*float64(h-1-y)/float64(h-1)))
			v := (float64(fr[bin]) + 20) / 70 // -20 dB → 0, +50 dB → 1
			img.Set(t, y, heat(math.Max(0, math.Min(1, v))))
		}
		for y := h; y < h+strip; y++ {
			img.Set(t, y, color.NRGBA{14, 17, 21, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func heat(v float64) color.NRGBA {
	// dark slate → teal → amber → near white
	stops := [][3]float64{{18, 22, 28}, {20, 110, 120}, {224, 173, 62}, {250, 245, 225}}
	p := v * float64(len(stops)-1)
	i := int(p)
	if i >= len(stops)-1 {
		i, p = len(stops)-2, float64(len(stops)-1)
	}
	f := p - float64(i)
	c := func(k int) uint8 { return uint8(stops[i][k] + (stops[i+1][k]-stops[i][k])*f) }
	return color.NRGBA{c(0), c(1), c(2), 255}
}
