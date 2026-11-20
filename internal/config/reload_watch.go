package config

// PollReload is the long-lived caller's hot-reload polling primitive: it
// checks whether the on-disk config differs from the snapshot cfg was loaded
// from (or last reloaded into) and, if so, rebuilds it with the same cwd, env
// and flag overrides.
//
// It is deliberately pure: no timers, no background goroutines, and no I/O
// beyond what Changed/Reload already perform (file stats and re-reading config
// files). Scheduling belongs to the caller — e.g. a long-lived engine can call
// this once per turn boundary.
//
// Return contract:
//
//   - No change: (cfg, false, nil). The current snapshot is still valid; the
//     caller keeps using it and nothing was reloaded.
//   - Change + successful reload: (next, true, nil). next is a fresh snapshot
//     whose Generation is cfg.Generation()+1; adopt it as the new current.
//   - Change + failed reload: (cfg, false, err). cfg is untouched and remains
//     the last known-good snapshot; err describes the reload failure.
//
// Because a successful Reload captures a fresh fingerprint, repeated polls of
// the returned snapshot are idempotent: once next is adopted, PollReload(next)
// reports no change until the disk changes again.
//
// PollReload only reads cfg (Changed and Reload both have value receivers and
// the reload metadata is immutable after load), so the same *Config may be
// polled concurrently from multiple goroutines. Callers that share a mutable
// "current config" field must still serialize who adopts the returned snapshot.
//
// A nil Config or one built by hand (not via Load) has no fingerprint to
// compare and is returned unchanged with changed=false.
func PollReload(cfg *Config) (*Config, bool, error) {
	if cfg == nil {
		return cfg, false, nil
	}
	if !cfg.Changed() {
		return cfg, false, nil
	}
	next, err := cfg.Reload()
	if err != nil {
		// Keep the last known-good snapshot on failure; adoption is the
		// caller's decision and a failed rebuild must not replace cfg.
		return cfg, false, err
	}
	return next, true, nil
}
