package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MutationSnapshot is the pre-turn state of one path in a checkpoint manifest.
// A missing file is recorded with Exists=false and an empty digest, so a
// checkpoint can distinguish "deleted by the turn" from "never existed".
type MutationSnapshot struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
	Exists bool   `json:"exists"`
}

// MutationPaths is the checkpoint seam: it reports the workspace-relative
// paths a mutating tool call can change. It is an optional interface on the
// registry so the agent loop can stay independent of the tools package.
func (r *Registry) MutationPaths(name, args string) []string {
	tool, ok := r.tools[name]
	if !ok || !tool.Mutating() {
		return nil
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(args), &fields); err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var paths []string
	collect := func(value any) {
		path, ok := value.(string)
		if !ok || strings.TrimSpace(path) == "" {
			return
		}
		absolute, err := resolve(r.root, path)
		if err != nil {
			// A denied escape is never a checkpoint path. The policy gate
			// reports it separately before the tool can run.
			return
		}
		rel, err := filepath.Rel(r.root, absolute)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return
		}
		clean := filepath.ToSlash(rel)
		if !seen[clean] {
			seen[clean] = true
			paths = append(paths, clean)
		}
	}
	for _, key := range []string{"path", "file", "dir", "directory", "target"} {
		collect(fields[key])
	}
	if list, ok := fields["paths"].([]any); ok {
		for _, item := range list {
			collect(item)
		}
	}
	sort.Strings(paths)
	return paths
}

// SnapshotMutations reads the pre-turn bytes of each path for a checkpoint
// manifest. Paths that lexically escape root are rejected rather than silently
// recorded.
func SnapshotMutations(root string, paths []string) ([]MutationSnapshot, error) {
	snapshots := make([]MutationSnapshot, 0, len(paths))
	for _, path := range paths {
		absolute, err := resolve(root, path)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, absolute)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(absolute)
		if err != nil {
			if os.IsNotExist(err) {
				snapshots = append(snapshots, MutationSnapshot{Path: filepath.ToSlash(rel), Exists: false})
				continue
			}
			return nil, err
		}
		digest := sha256.Sum256(data)
		snapshots = append(snapshots, MutationSnapshot{
			Path:   filepath.ToSlash(rel),
			SHA256: hex.EncodeToString(digest[:]),
			Exists: true,
		})
	}
	return snapshots, nil
}
