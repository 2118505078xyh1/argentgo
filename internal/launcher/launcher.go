// Package launcher opens the GUI in a dedicated Chromium app window instead
// of reusing a regular browser profile. It deliberately stays tiny: locate a
// browser binary, start it detached with its own profile, and tell the caller
// when it had to fall back to the OS default opener.
package launcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// ErrFallback is returned after the URL has been handed to the OS default
// browser. Callers match it with errors.Is to print a "browser tab" hint
// instead of the usual dedicated-window message.
var ErrFallback = errors.New("launcher: opened in default browser")

// Injectable seams. Tests replace both so no real browser is ever spawned.
var (
	lookPath = exec.LookPath
	start    = func(cmd *exec.Cmd) error { return cmd.Start() }
)

var (
	nameMu   sync.RWMutex
	lastName = "browser"
)

// candidate pairs an executable with the Name() value it selects.
type candidate struct {
	path string
	name string
}

// Open starts url in a dedicated browser app window backed by profileDir. The
// child is detached from ctx and from the CLI lifetime: Open returns as soon
// as the process has been spawned and never waits for it to exit.
//
// If no suitable binary is installed, Open uses the OS default opener and
// returns ErrFallback so the caller can tell the user it opened a browser tab.
func Open(ctx context.Context, url, profileDir string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	for _, c := range namedCandidates() {
		path, err := lookPath(c.path)
		if err != nil || path == "" {
			continue
		}
		cmd := exec.Command(path, appArgs(url, profileDir)...)
		prepare(cmd)
		if err := start(cmd); err != nil {
			return fmt.Errorf("launcher: start %s: %w", c.name, err)
		}
		reap(cmd)
		setName(c.name)
		return nil
	}
	return openFallback(url)
}

// Name reports the browser selected by the most recent Open. It is "edge" or
// "chrome" for a dedicated app window and "browser" for the default opener.
func Name() string {
	nameMu.RLock()
	defer nameMu.RUnlock()
	return lastName
}

// Candidates returns, in priority order, the executables Open probes on the
// current platform.
func Candidates() []string {
	cs := namedCandidates()
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.path)
	}
	return out
}

func setName(name string) {
	nameMu.Lock()
	lastName = name
	nameMu.Unlock()
}

// appArgs are the switches that give the GUI its own profile, taskbar entry
// and a predictable window size on every Chromium-family browser.
func appArgs(url, profileDir string) []string {
	return []string{
		"--app=" + url,
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--window-size=1280,860",
	}
}

func openFallback(url string) error {
	cmd := exec.Command(defaultOpener(), defaultOpenerArgs(url)...)
	prepare(cmd)
	if err := start(cmd); err != nil {
		return fmt.Errorf("launcher: default browser: %w", err)
	}
	reap(cmd)
	setName("browser")
	return ErrFallback
}

// reap releases the child handle without making Open wait for the browser to
// exit. Injected starts leave Process nil, so tests never create a goroutine.
func reap(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	go func() { _ = cmd.Wait() }()
}

func namedCandidates() []candidate {
	switch runtime.GOOS {
	case "windows":
		return windowsCandidates(os.Getenv("LOCALAPPDATA"))
	case "darwin":
		return macCandidates()
	default:
		return linuxCandidates()
	}
}

// windowsCandidates checks Edge first, then Chrome, so an Edge install wins
// on Windows while a Chrome-only machine still gets an app window.
func windowsCandidates(local string) []candidate {
	out := []candidate{
		{`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`, "edge"},
		{`C:\Program Files\Microsoft\Edge\Application\msedge.exe`, "edge"},
		{`C:\Program Files\Google\Chrome\Application\chrome.exe`, "chrome"},
		{`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`, "chrome"},
	}
	if local != "" {
		out = append(out, candidate{
			path: filepath.Join(local, "Google", "Chrome", "Application", "chrome.exe"),
			name: "chrome",
		})
	}
	return out
}

func macCandidates() []candidate {
	return []candidate{{`/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`, "chrome"}}
}

func linuxCandidates() []candidate {
	return []candidate{
		{"chromium", "chrome"},
		{"google-chrome", "chrome"},
		{"chromium-browser", "chrome"},
	}
}
