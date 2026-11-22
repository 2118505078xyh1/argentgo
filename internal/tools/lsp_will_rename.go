package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// 本文件提供 lsp_will_rename 工具：文件级重命名前询问语言服务器，
// 返回它建议的跨文件编辑（writethrough）。显式注册，不进默认 golden。

const lspWillRenameName = "lsp_will_rename"

// willRenameIde 是 lsp_will_rename 需要的窄能力，由 app 适配器实现。
// 定义在消费方，避免 tools import app/lsp 形成环。
type willRenameIde interface {
	WillRenameFiles(oldPath, newPath string) (*LspWorkspaceEdit, error)
}

type lspWillRenameTool struct {
	ide willRenameIde
}

func (t *lspWillRenameTool) Name() string { return lspWillRenameName }
func (t *lspWillRenameTool) Description() string {
	return "Ask the language server for the cross-file edits it would apply when renaming a file (import updates, reference rewrites). Read-only: returns proposed edits as text; apply them with edit/apply_patch."
}
func (t *lspWillRenameTool) Mutating() bool { return false }
func (t *lspWillRenameTool) Schema() string {
	return `{"type":"object","properties":{"oldPath":{"type":"string","description":"current file path"},"newPath":{"type":"string","description":"new file path"}},"required":["oldPath","newPath"]}`
}

func (t *lspWillRenameTool) Run(_ context.Context, root string, raw json.RawMessage) (string, error) {
	if t.ide == nil {
		return lspIdeUnavailable, nil
	}
	var args struct {
		OldPath string `json:"oldPath"`
		NewPath string `json:"newPath"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("lsp_will_rename: bad arguments: %w", err)
	}
	if args.OldPath == "" || args.NewPath == "" {
		return "", fmt.Errorf("lsp_will_rename: oldPath and newPath are required")
	}
	edit, err := t.ide.WillRenameFiles(args.OldPath, args.NewPath)
	if err != nil {
		return "", fmt.Errorf("lsp_will_rename: %w", err)
	}
	if edit == nil {
		return "(no edits suggested by the language server)", nil
	}
	data, err := json.Marshal(edit)
	if err != nil {
		return "", err
	}
	return clip(context.Background(), string(data)), nil
}

// SetLspWillRename 把适配器注入到工具的 ide 字段，遵循 SetLspIde 的原位置重建模式。
// 参数用 any 承载，使 tools 包不必 import app；类型断言失败时工具保持不可用。
func (r *Registry) SetLspWillRename(adapter any) {
	if r == nil {
		return
	}
	ide, _ := adapter.(willRenameIde)
	tool := &lspWillRenameTool{ide: ide}
	if existing, ok := r.tools[lspWillRenameName].(*lspWillRenameTool); ok {
		existing.ide = ide
		return
	}
	if _, exists := r.tools[lspWillRenameName]; !exists {
		r.order = append(r.order, lspWillRenameName)
	}
	r.tools[lspWillRenameName] = tool
}

// RegisterLspWillRename 显式注册工具（先于注入时 ide 为 nil，报告不可用）。
func RegisterLspWillRename(r *Registry) {
	if r == nil {
		return
	}
	if _, exists := r.tools[lspWillRenameName]; exists {
		return
	}
	r.tools[lspWillRenameName] = &lspWillRenameTool{}
	r.order = append(r.order, lspWillRenameName)
}
