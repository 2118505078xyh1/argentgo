package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// checkpointSource adapts the session checkpoint store to the consumer-side
// tools.CheckpointSource interface. It lives in app so internal/tools does not
// need to import internal/session (layering rule enforced by internal/archguard).
type checkpointSource struct {
	sess *session.Session
	root string
}

// Diff returns a per-file summary of the latest checkpoint. An empty id means
// "latest"; a positive id is parsed as a step-back count.
func (c *checkpointSource) Diff(id string) ([]tools.FileChange, error) {
	if c == nil || c.sess == nil {
		return nil, fmt.Errorf("no session available")
	}
	steps := 0
	if id != "" && id != "latest" {
		if _, err := fmt.Sscanf(id, "%d", &steps); err != nil || steps < 0 {
			return nil, fmt.Errorf("bad checkpoint id %q", id)
		}
	}
	cp, _, err := c.sess.CheckpointBack(steps)
	if err != nil {
		return nil, err
	}
	out := make([]tools.FileChange, 0, len(cp.Files))
	for _, f := range cp.Files {
		before := f.Before
		after, err := os.ReadFile(filepath.Join(c.root, f.Path))
		if err != nil {
			after = nil
		}
		out = append(out, tools.FileChangeFromBytes(f.Path, before, after))
	}
	return out, nil
}

// wireCheckpointDiff attaches the checkpoint_diff tool to a registry once the
// session and workspace root are known.
func wireCheckpointDiff(registry *tools.Registry, sess *session.Session, root string) {
	if registry == nil || sess == nil {
		return
	}
	registry.SetCheckpointSource(&checkpointSource{sess: sess, root: root})
}
