package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ---- multi_edit ----

type multiEditTool struct{}

func (multiEditTool) Name() string { return "multi_edit" }
func (multiEditTool) Description() string {
	return "Apply several old/new replacements to one file in order. All edits must match, otherwise nothing is written."
}
func (multiEditTool) Mutating() bool { return true }
func (multiEditTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"},"repair":{"type":"boolean","description":"opt-in graded repair ladder for missing or non-literal matches"},"relocate":{"type":"boolean","description":"opt-in read-only diagnosis of a mistyped filename; reports where the old text lives and leaves files untouched"},"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"},"old_text":{"type":"string","description":"alias for old"},"new_text":{"type":"string","description":"alias for new"}},"required":["old"]}}},"required":["path","edits"]}`
}

func (multiEditTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path     string `json:"path"`
		Repair   bool   `json:"repair"`
		Relocate bool   `json:"relocate"`
		Edits    []struct {
			Old     string `json:"old"`
			New     string `json:"new"`
			OldText string `json:"old_text"`
			NewText string `json:"new_text"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("multi_edit: bad arguments: %w", err)
	}
	if len(args.Edits) == 0 {
		return "", fmt.Errorf("multi_edit: edits must not be empty")
	}
	path, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	unlock := lockFileMutation(path)
	defer unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	// Validate and apply every edit in memory first; the file is written once.
	// A failed repair batch writes nothing: multi_edit stays atomic.
	text := string(data)
	applied := 0
	for i, edit := range args.Edits {
		old := edit.Old
		if old == "" {
			old = edit.OldText
		}
		newText := edit.New
		if newText == "" {
			newText = edit.NewText
		}
		if old == "" {
			return "", fmt.Errorf("multi_edit: edit %d has an empty old text", i+1)
		}
		if args.Repair {
			// repairReplace tries the exact match first and only falls back to
			// the fuzzy rungs when the literal is missing or non-unique.
			out, ok := repairReplace(text, old, newText)
			if !ok {
				err := multiEditRepairError(args.Path, i+1, len(args.Edits), applied, text, old)
				if args.Relocate {
					if hint := multiEditRelocateHint(root, path, old, newText); hint != "" {
						err = fmt.Errorf("%w%s", err, hint)
					}
				}
				return "", err
			}
			text = out
			applied++
			continue
		}
		count := strings.Count(text, old)
		switch count {
		case 0:
			hint := ""
			if args.Relocate {
				hint = multiEditRelocateHint(root, path, old, newText)
			}
			return "", fmt.Errorf("multi_edit: edit %d: old text not found in %s%s", i+1, args.Path, hint)
		case 1:
		default:
			// Keep the Rust-compatible count phrase for the eval suite.
			return "", fmt.Errorf("multi_edit: edit %d: old text appears %d times in %s (not unique); a unique match is required", i+1, count, args.Path)
		}
		text = strings.Replace(text, old, newText, 1)
	}

	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("edited %s (%d edits)", args.Path, len(args.Edits)), nil
}

// multiEditRelocateMaxFile caps how large a workspace file may be before the
// read-only relocation scan skips it, so one huge file cannot dominate a
// diagnosis.
const multiEditRelocateMaxFile = 4 << 20 // 4 MiB

// multiEditRelocateHint is the opt-in, read-only half of wrong-filename
// recovery. It snapshots the workspace, runs planMultiEdit on the single
// failing block and, when the SEARCH text lives literally (or only via the
// repair ladder) in a different file, reports those candidate paths. It never
// writes and never auto-picks a winner: guessing which file the model meant
// could silently edit a file it did not name. An empty return means "nothing
// useful to add", so callers can append the hint to an existing error without
// changing the default (relocate off) output at all.
func multiEditRelocateHint(root, intendedPath, old, newText string) string {
	if old == "" {
		return ""
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return ""
	}

	known := make(multiEditKnownFiles)
	_ = filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > multiEditRelocateMaxFile {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil || !utf8.Valid(data) {
			return nil
		}
		known[canonicalEditPath(p)] = string(data)
		return nil
	})
	if len(known) == 0 {
		return ""
	}

	plan := planMultiEdit([]multiEditBlock{{Path: intendedPath, Old: old, New: newText}},
		multiEditSnapshotResolver{files: known})
	var cands []multiEditFileCandidate
	for _, f := range plan.Failed {
		if f.Kind == multiEditWrongFile || f.Kind == multiEditNeedsRepair {
			cands = append(cands, f.Candidates...)
		}
	}
	if len(cands) == 0 {
		return ""
	}

	parts := make([]string, 0, len(cands))
	for _, c := range cands {
		rel, rerr := filepath.Rel(absRoot, filepath.FromSlash(c.Path))
		if rerr != nil {
			rel = c.Path
		}
		rel = filepath.ToSlash(rel)
		if c.ViaRepair {
			rel += " (fuzzy match)"
		}
		parts = append(parts, rel)
	}
	return "; possible filename typo: the old text was found in " + strings.Join(parts, ", ") +
		" (none were modified; correct the path and re-send)"
}
