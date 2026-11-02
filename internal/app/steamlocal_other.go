//go:build !windows

package app

func newLocalSteam() localSteamReader { return nil }
