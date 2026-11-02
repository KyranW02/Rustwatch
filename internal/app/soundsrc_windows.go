//go:build windows

package app

import (
	"rustwatch/internal/sound"
	"rustwatch/internal/wasapi"
)

// winAudio records Rust's own audio (or all PC audio on older Windows).
type winAudio struct{}

func newGameAudio() audioSource { return winAudio{} }

func (winAudio) Ready() (bool, string) {
	if rustPID() == 0 {
		return false, "Waiting for Rust to start."
	}
	return true, ""
}

func (winAudio) Run(stop <-chan struct{}, feed func(l, r []float32), started func(string)) error {
	pid := rustPID()
	if pid == 0 {
		return nil
	}
	var rl, rr *sound.Resampler
	rate, ch := 0, 0
	var bl, br []float32
	return wasapi.Capture(stop, pid, true, func(s []float32, r, c int) {
		if rl == nil || r != rate || c != ch {
			rl, rr, rate, ch = sound.NewResampler(r, 1), sound.NewResampler(r, 1), r, c
		}
		// front left / front right (the first two channels in every Windows layout)
		bl, br = bl[:0], br[:0]
		for i := 0; i+c <= len(s); i += c {
			bl = append(bl, s[i])
			if c > 1 {
				br = append(br, s[i+1])
			} else {
				br = append(br, s[i])
			}
		}
		ol, or := rl.Write(bl), rr.Write(br)
		if n := min(len(ol), len(or)); n > 0 {
			feed(ol[:n], or[:n])
		}
	}, func(m wasapi.Mode) { started(string(m)) })
}
