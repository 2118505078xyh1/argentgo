package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const checkpointDiffUnavailable = "checkpoint diff unavailable: no checkpoint source configured"

// FileChange is the neutral per-file diff summary rendered by checkpoint_diff.
// It mirrors only what a checkpoint record can provide (paths, before/after
// hashes, optional line counts) so this package does not need to import
// internal/session.
type FileChange struct {
	Path         string `json:"path"`
	Status       string `json:"status,omitempty"` // modified | added | deleted | unchanged
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256,omitempty"`
	Added        int    `json:"added,omitempty"`
	Removed      int    `json:"removed,omitempty"`
}

// CheckpointSource supplies checkpoint diff data. It is declared here, on the
// consumer side, so internal/tools stays independent of internal/session.
//
// The app adapter wraps a session and the workspace root: find the checkpoint
// by id (empty means latest), map each session.CheckpointFile to a FileChange
// with FileChangeFromBytes using f.Before and the current workspace bytes, and
// return the slice. Register the adapter with Registry.SetCheckpointSource.
type CheckpointSource interface {
	Diff(id string) ([]FileChange, error)
}

// SetCheckpointSource registers the read-only checkpoint_diff tool. Call it
// from internal/app once the session directory is known. Passing nil registers
// a tool that reports "checkpoint diff unavailable" instead of failing the run.
func (r *Registry) SetCheckpointSource(src CheckpointSource) {
	t := newCheckpointDiffTool(src)
	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

type checkpointDiffTool struct {
	source CheckpointSource
}

var _ Tool = (*checkpointDiffTool)(nil)

func newCheckpointDiffTool(source CheckpointSource) *checkpointDiffTool {
	return &checkpointDiffTool{source: source}
}

func (checkpointDiffTool) Name() string { return "checkpoint_diff" }

func (checkpointDiffTool) Description() string {
	return "Summarize a checkpoint's per-file changes: paths, before/after SHA-256 and added/removed lines. Read-only."
}

func (checkpointDiffTool) Mutating() bool { return false }

func (checkpointDiffTool) Schema() string {
	return `{"type":"object","properties":{"checkpoint_id":{"type":"string","description":"checkpoint record id; empty selects the latest"},"root":{"type":"string","description":"optional workspace root override; defaults to the tool root"}},"required":["checkpoint_id"]}`
}

func (t *checkpointDiffTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		CheckpointID string `json:"checkpoint_id"`
		Root         string `json:"root"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("checkpoint_diff: bad arguments: %w", err)
	}
	// The workspace root is bound into the adapter at construction time. The
	// optional root field is accepted for schema compatibility but not needed
	// here, since Tool.Run already receives the authoritative registry root.
	_ = args.Root
	_ = root

	if t.source == nil {
		return checkpointDiffUnavailable, nil
	}
	changes, err := t.source.Diff(args.CheckpointID)
	if err != nil {
		return checkpointDiffUnavailable + ": " + err.Error(), nil
	}
	return clipCheckpointDiff(ctx, renderCheckpointDiff(changes)), nil
}

const checkpointDiffMarker = "\n... [truncated] ...\n"

// clipCheckpointDiff is a strictly bounded clip: clipTo adds a marker on top of
// its head/tail budget, so the caller's limit is reduced by the marker size to
// keep the whole output (marker included) within the configured cap.
func clipCheckpointDiff(ctx context.Context, text string) string {
	limit := outputLimit(ctx) - len(checkpointDiffMarker)
	if limit < 1 {
		limit = 1
	}
	return clipTo(text, limit)
}

type checkpointDiffOutput struct {
	Count int          `json:"count"`
	Files []FileChange `json:"files"`
}

// renderCheckpointDiff emits compact JSON with a stable path order so the
// model sees a deterministic summary regardless of source iteration order.
func renderCheckpointDiff(changes []FileChange) string {
	files := make([]FileChange, 0, len(changes))
	files = append(files, changes...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	out := checkpointDiffOutput{Count: len(files), Files: files}
	data, err := json.Marshal(out)
	if err != nil {
		return checkpointDiffUnavailable + ": encode diff"
	}
	return string(data)
}

// FileChangeFromBytes builds a FileChange from the pre/post bytes a checkpoint
// adapter has at hand. A nil slice means the file was absent on that side; an
// empty non-nil slice is an empty file that exists.
func FileChangeFromBytes(path string, before, after []byte) FileChange {
	change := FileChange{Path: path, Status: "modified"}
	switch {
	case before == nil && after != nil:
		change.Status = "added"
	case before != nil && after == nil:
		change.Status = "deleted"
	}
	if before != nil {
		sum := sha256.Sum256(before)
		change.BeforeSHA256 = hex.EncodeToString(sum[:])
	}
	if after != nil {
		sum := sha256.Sum256(after)
		change.AfterSHA256 = hex.EncodeToString(sum[:])
	}
	change.Added, change.Removed = DiffLineCounts(before, after)
	return change
}

const maxDiffLinesForLCS = 4000

// DiffLineCounts returns (added, removed) line counts between before and
// after. It uses a bounded LCS so unchanged lines are not counted as edits;
// for very large inputs it falls back to whole-file counts rather than
// allocating a quadratic table. CRLF is normalized to LF so a line-ending
// change alone does not inflate the result.
func DiffLineCounts(before, after []byte) (added, removed int) {
	a := splitDiffLines(before)
	b := splitDiffLines(after)
	if len(a) > maxDiffLinesForLCS || len(b) > maxDiffLinesForLCS ||
		len(a)*len(b) > maxDiffLinesForLCS*maxDiffLinesForLCS {
		return len(b), len(a)
	}
	// dp[i][j] is the LCS length of a[:i] and b[:j].
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	common := dp[len(a)][len(b)]
	return len(b) - common, len(a) - common
}

func splitDiffLines(data []byte) []string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
