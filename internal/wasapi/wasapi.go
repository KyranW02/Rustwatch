// Package wasapi records what a Windows program is playing: one process's audio (process
// loopback, Windows 10 build 20348+ / Windows 11) or, failing that, everything the default
// speakers play (system loopback). Plain syscalls and COM vtables, no cgo.
package wasapi

import "errors"

// Mode says what is being recorded.
type Mode string

const (
	ModeProcess Mode = "process" // only the target program
	ModeSystem  Mode = "system"  // everything the PC plays
)

// OnAudio receives interleaved float32 samples.
type OnAudio func(samples []float32, rate, channels int)

var ErrUnsupported = errors.New("audio capture is only available on Windows")
