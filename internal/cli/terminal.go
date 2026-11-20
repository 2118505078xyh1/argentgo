package cli

import "os"

// hasTerminal reports whether the TUI has a terminal to attach to. Either
// stdin or stdout being interactive is enough: when only stdout is a terminal
// (e.g. `echo foo | argent tui`), the frontend can reopen the controlling TTY
// for input. The injected isTerminal lets tests exercise the gate without a
// real console.
func hasTerminal(isTerminal func(uintptr) bool, stdin, stdout uintptr) bool {
	return isTerminal(stdin) || isTerminal(stdout)
}

// defaultHasTerminal is the real platform check wired in by OSIO. It mirrors
// the guard at the top of Crush's root command: without it, an invocation with
// redirected streams and no console would still load config and start a
// full-screen UI.
func defaultHasTerminal() bool {
	return hasTerminal(isTerminal, os.Stdin.Fd(), os.Stdout.Fd())
}

// hasTerminal reports whether this App has an interactive terminal. IO built
// by tests can stub the check; a nil seam means "no terminal available", which
// keeps terminal-only commands from touching config or a real console.
func (a App) hasTerminal() bool {
	if a.IO.HasTerminal != nil {
		return a.IO.HasTerminal()
	}
	return false
}
