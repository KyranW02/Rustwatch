//go:build !windows

package app

func newLocalSteam() localSteamReader { return nil }

func rustPID() uint32 { return 0 }

func rustRoots() []string { return nil }
