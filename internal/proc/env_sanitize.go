package proc

import (
	"os"
	"runtime"
	"strings"
)

// hostPrivateEnvNames are environment variables that carry host session
// plumbing into a child process. A nested argent agent that inherits them could
// mistake them for its own session state, so they are dropped from the
// inherited environment before a child is spawned.
var hostPrivateEnvNames = []string{
	"ARGENT_SESSION_ID",
	"ARGENT_RUN_ID",
}

// hostPrivateEnvPrefixes are name prefixes whose variables are host-private for
// the same reason: ARGENT_PARENT_* and SUBAGENT_PARENT_* are written by the host
// when it launches a subagent.
var hostPrivateEnvPrefixes = []string{
	"ARGENT_PARENT_",
	"SUBAGENT_PARENT_",
}

// SanitizeEnv returns the environment a child process should receive: the
// parent's environment with host-private variables removed, followed by extra.
// Entries in extra are appended verbatim and are never filtered, so an explicit
// per-call value always wins over anything inherited. Order is preserved.
//
// Other child-spawn sites in the tree (jobs, MCP, LSP, pty, hooks) can adopt
// this primitive later; for now only proc.Run calls it.
func SanitizeEnv(extra []string) []string {
	parent := os.Environ()
	out := make([]string, 0, len(parent)+len(extra))
	for _, entry := range parent {
		if isHostPrivateEnvEntry(entry) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, extra...)
}

// isHostPrivateEnvEntry reports whether an inherited entry's key is on the
// host-private denylist. The key is everything before the FIRST '=' (or the
// whole entry for a bare "KEY"), so a value containing '=' cannot confuse the
// split.
func isHostPrivateEnvEntry(entry string) bool {
	key := entry
	if i := strings.IndexByte(entry, '='); i >= 0 {
		key = entry[:i]
	}
	for _, name := range hostPrivateEnvNames {
		if envNamesEqual(key, name) {
			return true
		}
	}
	for _, prefix := range hostPrivateEnvPrefixes {
		if envNameHasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// envNamesEqual compares two environment variable names. Windows treats names
// case-insensitively; other platforms treat them case-sensitively.
func envNamesEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// envNameHasPrefix reports whether key starts with prefix under the platform's
// name-comparison rules.
func envNameHasPrefix(key, prefix string) bool {
	if runtime.GOOS == "windows" {
		return strings.HasPrefix(strings.ToLower(key), strings.ToLower(prefix))
	}
	return strings.HasPrefix(key, prefix)
}
