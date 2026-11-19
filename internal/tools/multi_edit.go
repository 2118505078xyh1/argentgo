package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ---- multi_edit ----

type multiEditTool struct{}

func (multiEditTool) Name() string { return "multi_edit" }
func (multiEditTool) Description() string {
	return "Apply several old/new replacements to one file in order. All edits must match, otherwise nothing is written."
}
func (multiEditTool) Mutating() bool { return true }
func (multiEditTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"},"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"},"old_text":{"type":"string","description":"alias for old"},"new_text":{"type":"string","description":"alias for new"}},"required":["old"]}}},"required":["path","edits"]}`
}

func (multiEditTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path  string `json:"path"`
		Edits []struct {
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
	text := string(data)
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
		count := strings.Count(text, old)
		switch count {
		case 0:
			return "", fmt.Errorf("multi_edit: edit %d: old text not found in %s", i+1, args.Path)
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
