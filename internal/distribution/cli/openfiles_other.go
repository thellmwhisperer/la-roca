//go:build !linux && !darwin

package cli

func openFilesOf(pid int) []string { return nil }
