package agent

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// Checkpoint describes one mutating tool batch before it runs.
//
// The agent package does not own the session manifest; it only supplies the
// turn, the calls and the workspace paths a caller should snapshot. A session
// layer can record pre-turn bytes and SHA-256 digests for Paths before the
// tools touch the workspace, then merge the result into its checkpoint file.
type Checkpoint struct {
	Turn  int              `json:"turn"`
	Calls []CheckpointCall `json:"calls"`
	Paths []string         `json:"paths"`
}

// CheckpointCall is one tool call included in a checkpoint.
type CheckpointCall struct {
	ID   string `json:"id,omitempty"`
	Tool string `json:"tool"`
	Args string `json:"args,omitempty"`
}

// MutationReporter is an optional interface a ToolRunner may implement to
// declare which workspace paths a call can change. Only paths inside the
// workspace are useful; the agent normalizes and de-duplicates them.
type MutationReporter interface {
	MutationPaths(name, args string) []string
}

// newCheckpoint builds the checkpoint for a batch. It returns nil when the
// batch contains no mutating call, so read-only batches stay completely quiet.
func (s *runState) newCheckpoint(turn int, calls []llm.ToolCall) *Checkpoint {
	cp := &Checkpoint{Turn: turn}
	seenPaths := make(map[string]bool)
	for _, call := range calls {
		name := call.Function.Name
		mutating := s.mutating(name)
		if !mutating {
			continue
		}
		cp.Calls = append(cp.Calls, CheckpointCall{
			ID:   call.ID,
			Tool: name,
			Args: call.Function.Arguments,
		})
		for _, path := range s.mutationPaths(name, call.Function.Arguments) {
			clean := normalizeMutationPath(path)
			if clean == "" || seenPaths[clean] {
				continue
			}
			seenPaths[clean] = true
			cp.Paths = append(cp.Paths, clean)
		}
	}
	if len(cp.Calls) == 0 {
		return nil
	}
	sort.Strings(cp.Paths)
	return cp
}

// mutationPaths consults the explicit hook first, then the tool runner's
// optional interface. The wrapped runner may hide that interface, so the
// original Options.Tools is used rather than s.tools.
func (s *runState) mutationPaths(name, args string) []string {
	if s.opts.MutationPaths != nil {
		return s.opts.MutationPaths(name, args)
	}
	if reporter, ok := s.opts.Tools.(MutationReporter); ok {
		return reporter.MutationPaths(name, args)
	}
	return nil
}

// normalizeMutationPath rejects absolute and escaping paths and returns a
// stable slash-separated relative path. Empty means "not a checkpoint path".
func normalizeMutationPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) {
		return ""
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	return clean
}
