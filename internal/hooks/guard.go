package hooks

import (
	"errors"
	"fmt"
)

// ErrCallbackPanic is the sentinel a PanicError wraps, so callers can use
// errors.Is to tell a panicking callback apart from an ordinary failure.
var ErrCallbackPanic = errors.New("hooks: callback panicked")

// PanicError is the structured result of a callback that panicked. CallID is
// the identifier of the call that was being dispatched; it survives the panic
// so the run can report which callback failed without aborting.
type PanicError struct {
	CallID string
	Value  any
}

// Error implements error.
func (e *PanicError) Error() string {
	return fmt.Sprintf("hooks: callback for call %q panicked: %v", e.CallID, e.Value)
}

// Unwrap exposes the ErrCallbackPanic sentinel.
func (e *PanicError) Unwrap() error { return ErrCallbackPanic }

// guard runs fn and converts a panic into a PanicError carrying callID. It is
// used at the hook dispatch boundary so one misbehaving callback cannot abort
// the whole run: the caller records the error and moves on to the next hook.
func guard(callID string, fn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &PanicError{CallID: callID, Value: recovered}
		}
	}()
	return fn()
}
