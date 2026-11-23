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
	data, err := readFileLimited(path, readMaxBytes)
	if err != nil {
		return "", err
	}

	// 先在内存中校验并应用每一处编辑；文件只写入一次。
	// 修复批次失败则什么也不写：multi_edit 保持原子性。
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
			// repairReplace 先尝试精确匹配，仅当字面量缺失或不唯一时
			// 才回退到模糊级别。
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
			// 保留与 Rust 兼容的计数短语，供 eval 套件使用。
			return "", fmt.Errorf("multi_edit: edit %d: old text appears %d times in %s (not unique); a unique match is required", i+1, count, args.Path)
		}
		text = strings.Replace(text, old, newText, 1)
	}

	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("edited %s (%d edits)", args.Path, len(args.Edits)), nil
}

// multiEditRelocateMaxFile 限制工作区文件在只读重定位扫描中
// 被跳过的大小上限，避免单个巨大文件主导
// 诊断结果。
const multiEditRelocateMaxFile = 4 << 20 // 4 MiB

// multiEditRelocateHint 是文件名写错恢复机制的 opt-in、只读部分。
// 恢复。它对工作区做快照，对单个失败块运行 planMultiEdit，当 SEARCH
// 文本字面存在（或仅通过修复阶梯）于另一个文件时，报告这些候选路径。它
// 从不写入，也从不自动选出胜者：猜测模型本意是哪个文件
// 可能会悄悄编辑一个它并未指名的文件。空返回表示"没有
// 可补充的有用信息"，因此调用方可以把提示附加到现有错误上，
// 而不会改变默认（重定位关闭）的输出。
// 完全不改变默认（重定位关闭）的输出。
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
