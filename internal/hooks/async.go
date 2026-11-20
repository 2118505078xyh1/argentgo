package hooks

import "context"

// planAsync selects the hooks marked Async for background execution. It draws
// from the same claimedCommands walk as planRun, so a command is claimed once
// across the whole event, a stop boundary shields later hooks in both paths,
// and the background and synchronous sets are disjoint. Async hooks never
// enter planRun's tiers, so the returned decision is unaffected by async work.
func planAsync(commands []Command) []Command {
	claimed := claimedCommands(commands)
	async := make([]Command, 0, len(claimed))
	for _, command := range claimed {
		if command.Async {
			async = append(async, command)
		}
	}
	return async
}

// launchAsync starts each background hook and returns without waiting for it.
//
// The spawned context is detached from the caller's cancellation with
// WithoutCancel: an async hook is fire-and-forget work that should survive the
// request that triggered it, even when that request's context is cancelled as
// soon as Run returns. Values (session ID, call ID, ...) are preserved. The
// hook's own timeout is still applied, so wall-clock time stays bounded.
//
// Panic containment, telemetry and the runner all come from guardedRunCommand,
// so a background hook is observed exactly like a synchronous one. A final
// recover in the goroutine keeps any unexpected failure from taking down the
// process; Done runs last so WaitAsync always drains.
func (d *Dispatcher) launchAsync(ctx context.Context, async []Command, payload Payload) {
	for _, command := range async {
		d.asyncWG.Add(1)
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.timeoutFor(command))
		go func(command Command, runCtx context.Context, cancel context.CancelFunc) {
			defer d.asyncWG.Done()
			defer cancel()
			defer func() { _ = recover() }()
			_ = d.guardedRunCommand(runCtx, command, payload)
		}(command, runCtx, cancel)
	}
}

// WaitAsync blocks until every async hook launched by Run on this dispatcher
// has finished. Run itself never calls it, so async work cannot delay the
// decision. Call it only after Run has returned, when no further hooks can be
// added; tests and graceful-shutdown paths use it to drain background work.
func (d *Dispatcher) WaitAsync() {
	d.asyncWG.Wait()
}
