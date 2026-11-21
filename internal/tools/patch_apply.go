package tools

import (
	"errors"
	"fmt"
	"strings"
)

// 本文件提供一个最小的、零依赖的补丁应用原语。它刻意不解析任何
// diff/patch 文本格式（那是调用方的职责），只接受已经结构化的
// SimplePatch：路径 + 一段必须唯一匹配的旧文本 + 新文本。当前
// 没有生产调用方，只有测试引用它；后续接入工具时可按需开启。

// SimplePatch 描述对单个文件的文本替换意图。
//
// Path 是目标文件路径；OldText 是必须在原内容中恰好出现一次的文本；
// NewText 是替换后的文本。Add 表示一次新增（原内容必须为空），
// Delete 表示一次删除（NewText 必须为空）。
type SimplePatch struct {
	Path    string
	OldText string
	NewText string
	Add     bool
	Delete  bool
}

// 补丁应用错误的哨兵值。所有返回的错误都用 %w 包装它们，
// 因此调用方可以用 errors.Is 匹配每一种失败。
var (
	ErrPatchNotUnique    = errors.New("tools: patch old text must appear exactly once")
	ErrPatchAddNotEmpty  = errors.New("tools: add patch requires empty original content")
	ErrPatchDeleteHasNew = errors.New("tools: delete patch requires empty new text")
	ErrPatchInvalidFlags = errors.New("tools: patch cannot be both add and delete")
	ErrPatchFileNotFound = errors.New("tools: patch target file not found")
)

// ApplySimplePatch 把 original 中的 OldText 替换为 NewText。
//
// 约束：
//   - OldText 必须在 original 中恰好出现一次，否则返回包装了
//     ErrPatchNotUnique 的错误（0 次与 2 次以上都算不唯一）；
//   - Add 为真时 original 必须为空；
//   - Delete 为真时 NewText 必须为空；
//   - Add 与 Delete 不能同时为真。
//
// 它是纯函数：字符串在 Go 中不可变，函数只返回新字符串，不修改
// 入参，也不做任何 I/O。字节级的精确匹配使 CJK 与 CRLF 内容安全
// ——不做任何编码转换或换行归一化。
func ApplySimplePatch(original string, p SimplePatch) (string, error) {
	if p.Add && p.Delete {
		return "", fmt.Errorf("%w: path %q", ErrPatchInvalidFlags, p.Path)
	}
	if p.Add && original != "" {
		return "", fmt.Errorf("%w: path %q", ErrPatchAddNotEmpty, p.Path)
	}
	if p.Delete && p.NewText != "" {
		return "", fmt.Errorf("%w: path %q", ErrPatchDeleteHasNew, p.Path)
	}

	n := strings.Count(original, p.OldText)
	if n != 1 {
		return "", fmt.Errorf("%w: path %q, found %d occurrences", ErrPatchNotUnique, p.Path, n)
	}
	return strings.Replace(original, p.OldText, p.NewText, 1), nil
}

// ApplyPatchBatch 按顺序原子地把多个补丁应用到文件快照上。
//
// files 以路径为键保存当前内容。返回值是一个全新的 map：只有在所有
// 补丁都成功时才返回它；任一补丁失败时返回 nil 结果和错误，不暴露
// 任何部分结果。入参 files 与 patches 永不被修改。
//
// 补丁按 patches 的切片顺序求值，因此前一个补丁的结果对后一个补丁
// 可见。对于 Add 补丁，目标路径可以尚不存在（视为空内容）；其余
// 补丁的目标路径必须存在，否则返回包装了 ErrPatchFileNotFound 的
// 错误。
func ApplyPatchBatch(files map[string]string, patches []SimplePatch) (map[string]string, error) {
	working := make(map[string]string, len(files)+len(patches))
	for path, content := range files {
		working[path] = content
	}

	for _, p := range patches {
		original, exists := working[p.Path]
		if !exists {
			if !p.Add {
				return nil, fmt.Errorf("%w: %q", ErrPatchFileNotFound, p.Path)
			}
			original = ""
		}
		updated, err := ApplySimplePatch(original, p)
		if err != nil {
			return nil, err
		}
		working[p.Path] = updated
	}
	return working, nil
}
