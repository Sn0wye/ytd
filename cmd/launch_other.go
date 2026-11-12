//go:build !windows

package cmd

// startedByDoubleClick is only detected on Windows, where double-clicking
// the binary in Explorer is the usual way to start it.
func startedByDoubleClick() bool { return false }
