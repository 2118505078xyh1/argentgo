// Package agent's before-hooks are optional durability seams. The agent loop
// does not register anything itself; the application (internal/app, which owns
// the session) wires them once at startup. A typical wiring that flushes the
// JSONL session to stable storage before each expensive step looks like:
//
//	sess := ...                              // *session.Session
//	agent.RegisterBeforeRequest(func() error { return sess.Sync() })
//	agent.RegisterBeforeTool(func() error    { return sess.Sync() })
//
// RegisterBeforeRequest / RegisterBeforeTool accept any func() error, so the
// application may also wrap Sync with its own context or logging. The loop
// calls RunBeforeRequest before chatWithFallback and RunBeforeTool before
// dispatching a tool batch; a non-nil error from either aborts that action.
// Keeping the registration in the app preserves the layering rule that the
// agent package takes interfaces/callbacks rather than importing the store.
package agent

import (
	"errors"
	"sync"
)

// BeforeRequestHook runs immediately before an expensive model request.
// BeforeToolHook runs immediately before a tool is dispatched.
//
// A hook returning an error aborts the current action: the agent loop treats a
// request-hook error like any other chat failure, and a tool-hook error like a
// failed tool result the model can read. A nil hook is simply skipped.
type (
	BeforeRequestHook func() error
	BeforeToolHook    func() error
)

var (
	beforeHooksMu      sync.RWMutex
	beforeRequestHooks []BeforeRequestHook
	beforeToolHooks    []BeforeToolHook
)

// RegisterBeforeRequest adds a hook that runs before every model request.
// Registering nil is a no-op. Hooks run in registration order.
func RegisterBeforeRequest(hook BeforeRequestHook) {
	if hook == nil {
		return
	}
	beforeHooksMu.Lock()
	beforeRequestHooks = append(beforeRequestHooks, hook)
	beforeHooksMu.Unlock()
}

// RegisterBeforeTool adds a hook that runs before every tool dispatch.
// Registering nil is a no-op. Hooks run in registration order.
func RegisterBeforeTool(hook BeforeToolHook) {
	if hook == nil {
		return
	}
	beforeHooksMu.Lock()
	beforeToolHooks = append(beforeToolHooks, hook)
	beforeHooksMu.Unlock()
}

// RunBeforeRequest runs all registered request hooks in order and returns the
// first error. It is safe to call concurrently with registration.
func RunBeforeRequest() error {
	beforeHooksMu.RLock()
	hooks := append([]BeforeRequestHook(nil), beforeRequestHooks...)
	beforeHooksMu.RUnlock()
	for _, hook := range hooks {
		if err := hook(); err != nil {
			return err
		}
	}
	return nil
}

// RunBeforeTool runs all registered tool hooks in order and returns the first
// error. It is safe to call concurrently with registration.
func RunBeforeTool() error {
	beforeHooksMu.RLock()
	hooks := append([]BeforeToolHook(nil), beforeToolHooks...)
	beforeHooksMu.RUnlock()
	for _, hook := range hooks {
		if err := hook(); err != nil {
			return err
		}
	}
	return nil
}

// ResetBeforeHooks removes all registered hooks. Intended for tests and for
// process shutdown; production callers should register once at startup.
func ResetBeforeHooks() {
	beforeHooksMu.Lock()
	beforeRequestHooks = nil
	beforeToolHooks = nil
	beforeHooksMu.Unlock()
}

// ErrBeforeHookAborted is a sentinel callers can wrap to stop a specific
// action from inside a before-hook (e.g. "session not durable yet").
var ErrBeforeHookAborted = errors.New("agent: before-hook aborted")
