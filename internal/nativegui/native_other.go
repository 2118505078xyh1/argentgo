//go:build !windows

package nativegui

import (
	"errors"

	"github.com/2118505078xyh1/argentgo/internal/gui"
)

// Options mirrors the Windows implementation so callers compile everywhere.
type Options struct {
	Cwd       string
	Model     string
	Provider  string
	Events    <-chan gui.Event
	Approvals <-chan gui.Approval
	Commands  chan<- gui.Command
}

// Run is Windows-only; the native GUI has no non-Windows implementation.
func Run(Options) error {
	return errors.New("nativegui: Windows only")
}
