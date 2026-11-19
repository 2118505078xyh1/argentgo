//go:build !windows && !linux && !darwin

package tui

// Stubs for platforms without a terminal backend here. The UI still builds and
// tests run; Run simply leaves the terminal alone.

func enableRaw() (func(), error) { return func() {}, nil }

func terminalSize() (int, int) { return 0, 0 }

func watchResize() (<-chan size, func()) {
	out := make(chan size)
	return out, func() {}
}
