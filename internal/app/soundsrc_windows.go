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

func (winAudio) Run(stop <-chan struct{}, feed func([]float32), started func(string)) error {
	pid := rustPID()
	if pid == 0 {
		return nil
	}
	var rs *sound.Resampler
	rate, ch := 0, 0
	return wasapi.Capture(stop, pid, true, func(s []float32, r, c int) {
		if rs == nil || r != rate || c != ch {
			rs, rate, ch = sound.NewResampler(r, c), r, c
		}
		if out := rs.Write(s); len(out) > 0 {
			feed(out)
		}
	}, func(m wasapi.Mode) { started(string(m)) })
}
