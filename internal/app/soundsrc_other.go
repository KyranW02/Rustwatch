//go:build !windows

package app

func newGameAudio() audioSource { return nil }
