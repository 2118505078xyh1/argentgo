//go:build !linux && !darwin && !windows

package cli

// isTerminal conservatively assumes a terminal on platforms without a
// specific probe, preserving the pre-guard behaviour there rather than
// disabling the TUI for everyone.
func isTerminal(uintptr) bool { return true }
