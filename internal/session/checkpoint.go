package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrCheckpointConflict reports that a file changed after the checkpoint was
// completed. Undo refuses to overwrite such a manual edit.
var ErrCheckpointConflict = errors.New("session: checkpoint conflict")

// ErrNoCheckpoint reports that a session has no checkpoint record.
var ErrNoCheckpoint = errors.New("session: no checkpoint in session")

// CheckpointFile holds one pre-turn snapshot. Path is always a slash-separated
// path relative to the workspace; SHA256 is the hash of the pre-turn bytes and
// Before carries those bytes so undo is self-contained. Absent means the file
// did not exist before the turn; the After fields describe the state recorded
// when the turn finished (including an aborted turn).
type CheckpointFile struct {
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	Before      []byte `json:"before,omitempty"`
	Absent      bool   `json:"absent,omitempty"`
	Mode        uint32 `json:"mode,omitempty"`
	AfterSHA256 string `json:"afterSha256,omitempty"`
	AfterAbsent bool   `json:"afterAbsent,omitempty"`
}

// Checkpoint is the payload of a kind:"checkpoint" record. Parent is the id of
// the record that was the active leaf before the mutating turn, so /retry can
// fork from exactly that point.
type Checkpoint struct {
	Parent  string           `json:"parent,omitempty"`
	Prompt  string           `json:"prompt,omitempty"`
	Files   []CheckpointFile `json:"files"`
	Partial bool             `json:"partial,omitempty"`
	Created time.Time        `json:"created,omitempty"`
}

// CaptureCheckpoint snapshots the pre-turn bytes of paths, which must be
// relative to workspace. Paths are deduplicated and sorted for a stable manifest.
func CaptureCheckpoint(workspace string, paths []string) (Checkpoint, error) {
	cp := Checkpoint{Files: make([]CheckpointFile, 0, len(paths)), Created: time.Now().UTC()}
	seen := make(map[string]bool, len(paths))
	for _, raw := range paths {
		rel, err := normalizeRelativePath(raw)
		if err != nil {
			return Checkpoint{}, err
		}
		if seen[rel] {
			continue
		}
		seen[rel] = true
		target, err := resolveWorkspacePath(workspace, rel)
		if err != nil {
			return Checkpoint{}, err
		}
		info, err := os.Lstat(target)
		file := CheckpointFile{Path: rel}
		if errors.Is(err, os.ErrNotExist) {
			file.Absent = true
		} else if err != nil {
			return Checkpoint{}, fmt.Errorf("session: stat %s: %w", rel, err)
		} else {
			if !info.Mode().IsRegular() {
				return Checkpoint{}, fmt.Errorf("session: checkpoint path %q is not a regular file", rel)
			}
			bytes, err := os.ReadFile(target)
			if err != nil {
				return Checkpoint{}, fmt.Errorf("session: read %s: %w", rel, err)
			}
			file.Before = bytes
			file.SHA256 = hashBytes(bytes)
			file.Mode = uint32(info.Mode().Perm())
		}
		cp.Files = append(cp.Files, file)
	}
	sort.Slice(cp.Files, func(i, j int) bool { return cp.Files[i].Path < cp.Files[j].Path })
	return cp, nil
}

// CompleteCheckpoint records the post-turn state of every manifest entry. It
// must run even when the turn was cancelled, with Partial set on the manifest.
func CompleteCheckpoint(workspace string, cp *Checkpoint) error {
	if cp == nil {
		return errors.New("session: nil checkpoint")
	}
	for i := range cp.Files {
		file := &cp.Files[i]
		target, err := resolveWorkspacePath(workspace, file.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			file.AfterAbsent = true
			file.AfterSHA256 = ""
			continue
		}
		if err != nil {
			return fmt.Errorf("session: stat %s: %w", file.Path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("session: checkpoint path %q became a non-regular file", file.Path)
		}
		bytes, err := os.ReadFile(target)
		if err != nil {
			return fmt.Errorf("session: read %s: %w", file.Path, err)
		}
		file.AfterAbsent = false
		file.AfterSHA256 = hashBytes(bytes)
	}
	return nil
}

// RecordCheckpoint appends a checkpoint to s and returns its stored record.
// Call CaptureCheckpoint and RecordCheckpoint before the mutating tool batch.
func (s *Session) RecordCheckpoint(cp Checkpoint) (Record, error) {
	records, err := s.Records()
	if err != nil {
		return Record{}, err
	}
	if path := s.activePath(records); len(path) > 0 {
		cp.Parent = path[len(path)-1].ID
	}
	cp.Created = time.Now().UTC()
	return s.AppendRecord(KindCheckpoint, cp)
}

// LatestCheckpoint returns the last checkpoint on the active branch and its
// record. It returns ErrNoCheckpoint when the session has none.
func (s *Session) LatestCheckpoint() (Checkpoint, Record, error) {
	records, err := s.Records()
	if err != nil {
		return Checkpoint{}, Record{}, err
	}
	path := s.activePath(records)
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].Kind != KindCheckpoint {
			continue
		}
		var cp Checkpoint
		if err := jsonUnmarshal(path[i].Data, &cp); err != nil {
			continue
		}
		return cp, path[i], nil
	}
	return Checkpoint{}, Record{}, ErrNoCheckpoint
}

// UndoCheckpoint restores only the files named in cp. A file whose current
// bytes match neither the recorded post-turn state nor the pre-turn state is a
// conflicting manual edit and is refused with ErrCheckpointConflict.
func UndoCheckpoint(workspace string, cp Checkpoint) ([]string, error) {
	type action struct {
		target string
		file   CheckpointFile
		remove bool
	}
	var actions []action
	for _, file := range cp.Files {
		rel, err := normalizeRelativePath(file.Path)
		if err != nil {
			return nil, err
		}
		target, err := resolveWorkspacePath(workspace, rel)
		if err != nil {
			return nil, err
		}
		info, statErr := os.Lstat(target)
		exists := statErr == nil
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("session: stat %s: %w", rel, statErr)
		}
		if exists && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return nil, fmt.Errorf("%w: %s is not a regular file", ErrCheckpointConflict, rel)
		}
		current := ""
		if exists {
			bytes, err := os.ReadFile(target)
			if err != nil {
				return nil, fmt.Errorf("session: read %s: %w", rel, err)
			}
			current = hashBytes(bytes)
		}
		if file.Absent {
			if !exists {
				continue
			}
			if file.AfterSHA256 == "" || current != file.AfterSHA256 {
				return nil, fmt.Errorf("%w: %s changed after the checkpoint", ErrCheckpointConflict, rel)
			}
			actions = append(actions, action{target: target, file: file, remove: true})
			continue
		}
		if !exists {
			if !file.AfterAbsent {
				return nil, fmt.Errorf("%w: %s was deleted after the checkpoint", ErrCheckpointConflict, rel)
			}
		} else if current != file.SHA256 {
			if file.AfterSHA256 == "" || current != file.AfterSHA256 {
				return nil, fmt.Errorf("%w: %s changed after the checkpoint", ErrCheckpointConflict, rel)
			}
		} else {
			continue // already at the pre-turn state
		}
		actions = append(actions, action{target: target, file: file})
	}

	restored := make([]string, 0, len(actions))
	for _, item := range actions {
		if item.remove {
			if err := os.Remove(item.target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return restored, fmt.Errorf("session: remove %s: %w", item.file.Path, err)
			}
			restored = append(restored, item.file.Path)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(item.target), 0o755); err != nil {
			return restored, fmt.Errorf("session: create parent of %s: %w", item.file.Path, err)
		}
		mode := os.FileMode(0o644)
		if item.file.Mode != 0 {
			mode = os.FileMode(item.file.Mode)
		}
		if err := os.WriteFile(item.target, item.file.Before, mode); err != nil {
			return restored, fmt.Errorf("session: restore %s: %w", item.file.Path, err)
		}
		if err := os.Chmod(item.target, mode); err != nil {
			return restored, fmt.Errorf("session: chmod %s: %w", item.file.Path, err)
		}
		restored = append(restored, item.file.Path)
	}
	return restored, nil
}

// RetryFromCheckpoint forks s at the checkpoint's parent and records a retried
// attempt on the fork. The source session file is never modified. An empty id
// selects the latest checkpoint on the active branch; a checkpoint record id
// or its parent record id are both accepted.
func (s *Session) RetryFromCheckpoint(id string) (*Session, error) {
	checkpoint, record, err := s.findCheckpoint(id)
	if err != nil {
		return nil, err
	}
	parent := checkpoint.Parent
	if parent == "" {
		path, err := s.Path(record.ID)
		if err != nil {
			return nil, err
		}
		if len(path) > 1 {
			parent = path[len(path)-2].ID
		} else if len(path) == 1 {
			parent = path[0].ID
		}
	}
	if parent == "" {
		return nil, ErrNoCheckpoint
	}
	fork, err := s.Fork(parent)
	if err != nil {
		return nil, err
	}
	_, err = fork.AppendAttempt(Attempt{
		Reason:        AttemptRetried,
		Message:       "forked from checkpoint " + record.ID,
		SourceSession: s.ID(),
		SourceRecord:  record.ID,
		Prompt:        checkpoint.Prompt,
	})
	if err != nil {
		return nil, err
	}
	return fork, nil
}

// Retry is the store-scoped form of RetryFromCheckpoint.
func Retry(dir, id, checkpointID string) (*Session, error) {
	sess, err := OpenSession(dir, id)
	if err != nil {
		return nil, err
	}
	return sess.RetryFromCheckpoint(checkpointID)
}

func (s *Session) findCheckpoint(id string) (Checkpoint, Record, error) {
	records, err := s.Records()
	if err != nil {
		return Checkpoint{}, Record{}, err
	}
	if id == "" {
		return s.LatestCheckpoint()
	}
	candidates := records
	if path := s.activePath(records); len(path) > 0 {
		candidates = path
	}
	for _, record := range candidates {
		if record.Kind != KindCheckpoint {
			continue
		}
		var cp Checkpoint
		if jsonUnmarshal(record.Data, &cp) != nil {
			continue
		}
		if record.ID == id || cp.Parent == id {
			return cp, record, nil
		}
	}
	return Checkpoint{}, Record{}, fmt.Errorf("session: no checkpoint %q in %s", id, s.ID())
}

// normalizeRelativePath converts a stored path to a slash-separated relative
// path. Absolute paths and .. segments are refused before any file is touched.
func normalizeRelativePath(raw string) (string, error) {
	rel := strings.TrimSpace(raw)
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("session: empty checkpoint path")
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" ||
		strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", fmt.Errorf("session: absolute checkpoint path %q", raw)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("session: checkpoint path escapes workspace: %q", raw)
	}
	return filepath.ToSlash(clean), nil
}

// resolveWorkspacePath maps a normalized relative path to an absolute path,
// rejecting symlinked components so a checkpoint can never restore through a
// link that points outside the workspace.
func resolveWorkspacePath(workspace, rel string) (string, error) {
	root := strings.TrimSpace(workspace)
	if root == "" {
		return "", errors.New("session: empty checkpoint workspace")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("session: resolve workspace: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	normalized, err := normalizeRelativePath(rel)
	if err != nil {
		return "", err
	}
	target := root
	for _, part := range strings.Split(filepath.FromSlash(normalized), string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		target = filepath.Join(target, part)
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("session: stat %s: %w", normalized, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("session: checkpoint path uses symlink: %q", normalized)
		}
	}
	relCheck, err := filepath.Rel(root, target)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("session: checkpoint path escapes workspace: %q", normalized)
	}
	return target, nil
}

func hashBytes(bytes []byte) string {
	sum := sha256.Sum256(bytes)
	return hex.EncodeToString(sum[:])
}

func jsonUnmarshal(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return errors.New("session: empty record payload")
	}
	return json.Unmarshal(raw, target)
}
