package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ---- write ----

type writeTool struct{}

func (writeTool) Name() string        { return "write" }
func (writeTool) Description() string { return "Create or overwrite a file with the given content." }
func (writeTool) Mutating() bool      { return true }
func (writeTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`
}

func (writeTool) Run(_ context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("write: bad arguments: %w", err)
	}
	path, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	unlock := lockFileMutation(path)
	defer unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(args.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path), nil
}

// ---- edit ----

type editTool struct{}

func (editTool) Name() string { return "edit" }
func (editTool) Description() string {
	return "Replace one unique occurrence of old text with new text."
}
func (editTool) Mutating() bool { return true }
func (editTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"},"old":{"type":"string"},"new":{"type":"string"},"old_text":{"type":"string"},"new_text":{"type":"string"}},"required":["path"]}`
}

func (editTool) Run(_ context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Old     string `json:"old"`
		New     string `json:"new"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("edit: bad arguments: %w", err)
	}
	if args.Old == "" {
		args.Old = args.OldText
	}
	if args.New == "" {
		args.New = args.NewText
	}
	if args.Old == "" {
		return "", fmt.Errorf("edit: old must not be empty")
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
	text := string(data)
	count := strings.Count(text, args.Old)
	switch count {
	case 0:
		return "", fmt.Errorf("edit: old text not found in %s", args.Path)
	case 1:
	default:
		// The count phrase matches the frozen Rust build's tool error, which
		// the eval suite asserts on; the rest explains what the model must do.
		return "", fmt.Errorf("edit: old text appears %d times in %s (not unique); a unique match is required", count, args.Path)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(text, args.Old, args.New, 1)), 0o644); err != nil {
		return "", err
	}
	return "edited " + args.Path, nil
}
