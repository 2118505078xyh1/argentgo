// Package tui is the full-screen terminal interface: an alternate-screen
// renderer over a cell Buffer that only repaints changed rows, the prompt
// editor, the scrollable transcript and the blocking approval modal.
//
// All UI state lives in Model, which never touches the terminal, so tests
// drive it directly. Run owns the terminal, the key decoder and the event
// loop, and restores the screen through a deferred cleanup.
package tui
