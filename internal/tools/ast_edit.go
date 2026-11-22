package tools

// ast_edit 工具：Go 源码的符号级结构替换。核心算法在无依赖的 astedit 包中，
// 本文件只做路径校验、预览渲染与可选的写盘助手注入。
//
// 默认注册的 ast_edit 不带写盘助手，是非变更性的预览工具：运行后只返回 -/+ diff，
// 不落盘，因此默认 golden 与非 yolo 模式都不受影响。只有显式注入写盘助手后
// 它才变为变更性工具，并由该助手自行走完整审批链（策略 → 审批 → 审计）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/astedit"
)

const (
	astEditToolName = "ast_edit"
	// astEditNotGoMsg 是非 .go 文件时的明确提示。
	astEditNotGoMsg = "该类型不支持结构编辑，请使用 edit/multi_edit"
)

// astEditWriter 是写盘助手契约，由接线层注入。实现方必须自行走完整审批链；
// ast_edit 本身绝不直接调用 os.WriteFile。
type astEditWriter func(root, path string, content []byte) error

// astEditTool 是 ast_edit 的工具封装。writer 为 nil 时纯预览。
type astEditTool struct {
	writer astEditWriter
}

var _ Tool = astEditTool{}

func (astEditTool) Name() string { return astEditToolName }

func (astEditTool) Description() string {
	return "Go 源码的符号级结构替换：按 AST 定位顶层声明或调用点并做字节区间改写，返回 -/+ diff。默认只预览不写盘。"
}

// Mutating 报告工具是否会写盘：只有注入了写盘助手才为变更性。
// 默认实例（preview 预览）是非变更性的，可在非 yolo 模式直接运行。
func (t astEditTool) Mutating() bool { return t.writer != nil }

func (astEditTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string","description":"目标 .go 文件路径。"},"symbol":{"type":"string","description":"要替换的顶层声明名或调用函数名。"},"replacement":{"type":"string","description":"替换文本。"},"preview":{"type":"boolean","description":"默认 true：只返回 diff 不写盘；false 时经写盘助手落盘。"}},"required":["path","symbol","replacement"]}`
}

// astEditToolArgs 是 Run 接受的 JSON 参数。preview 用指针区分"未传"与显式 false。
type astEditToolArgs struct {
	Path        string `json:"path"`
	Symbol      string `json:"symbol"`
	Replacement string `json:"replacement"`
	Preview     *bool  `json:"preview"`
}

func (t astEditTool) Run(_ context.Context, root string, raw json.RawMessage) (string, error) {
	var args astEditToolArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("ast_edit: bad arguments: %w", err)
	}

	// 非 Go 文件明确拒绝：结构编辑只对 Go 源码有意义。
	if !strings.EqualFold(filepath.Ext(args.Path), ".go") {
		return "", fmt.Errorf("ast_edit: %s", astEditNotGoMsg)
	}

	abs, err := resolve(root, args.Path)
	if err != nil {
		return "", fmt.Errorf("ast_edit: resolve %q: %w", args.Path, err)
	}

	// 有界读取：先查大小再加 1 字节上限读取，避免大文件进入内存。
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("ast_edit: open %q: %w", args.Path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("ast_edit: stat %q: %w", args.Path, err)
	}
	if info.Size() > astedit.MaxSourceBytes {
		return "", fmt.Errorf("ast_edit: %w: %q is %d bytes", astedit.ErrTooLarge, args.Path, info.Size())
	}
	src, err := io.ReadAll(io.LimitReader(f, astedit.MaxSourceBytes+1))
	if err != nil {
		return "", fmt.Errorf("ast_edit: read %q: %w", args.Path, err)
	}

	newSrc, err := astedit.RewriteSymbol(src, args.Symbol, []byte(args.Replacement))
	if err != nil {
		return "", fmt.Errorf("ast_edit: %w", err)
	}
	diff := astedit.Diff(src, newSrc)

	preview := true
	if args.Preview != nil {
		preview = *args.Preview
	}
	if preview {
		// 预览模式：只把新内容以 diff 呈现，绝不写盘。
		return "ast_edit preview (未写盘)\n" + diff, nil
	}
	if t.writer == nil {
		// 无写盘助手：不自建写入路径，交回 diff 让模型走 apply_patch 审批链。
		return "ast_edit preview (未写盘；未配置写盘助手，请使用 apply_patch 应用以下 diff)\n" + diff, nil
	}
	if err := t.writer(root, args.Path, newSrc); err != nil {
		return "", fmt.Errorf("ast_edit: write %q: %w", args.Path, err)
	}
	return fmt.Sprintf("ast_edit: rewrote symbol %q in %s\n%s", args.Symbol, args.Path, diff), nil
}

// RegisterAstEdit 把 ast_edit 显式追加到 r 的末尾（opt-in）。
// 幂等：重复调用或已存在同名工具时不再插入；既有顺序不变。nil 是空操作。
// 本函数绝不被 New/NewWithLimit 调用，因此默认 Specs()/Names() 与 golden 不变。
func RegisterAstEdit(r *Registry) {
	if r == nil {
		return
	}
	const name = astEditToolName
	if _, exists := r.tools[name]; exists {
		return
	}
	r.tools[name] = astEditTool{}
	r.order = append(r.order, name)
}

// SetAstEditWriter 为 ast_edit 注入写盘助手，使其具备写盘能力（同时变为变更性）。
// 遵循 SetCodeGraph 的原位置重建模式：注册顺序与公布的 spec 保持稳定。
// nil 助手恢复默认纯预览行为；若尚未注册则先完成注册。
func (r *Registry) SetAstEditWriter(w astEditWriter) {
	if r == nil {
		return
	}
	const name = astEditToolName
	if _, exists := r.tools[name]; !exists {
		r.tools[name] = astEditTool{}
		r.order = append(r.order, name)
	}
	r.tools[name] = astEditTool{writer: w}
	r.wrapSchemas()
}
