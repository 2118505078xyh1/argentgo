package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// apply_patch 相关哨兵错误。它们都包裹 ErrApplyPatch，调用方可用
// errors.Is 做分层匹配：先判大类，再判"锚点歧义""锚点缺失"或"文件缺失"。
var (
	// ErrApplyPatch 标记 apply_patch 工具的结构性或应用性失败。
	ErrApplyPatch = errors.New("tools: apply_patch failed")
	// ErrApplyPatchAmbiguous 标记 hunk 的旧文本锚点在文件中出现多次，
	// 无法唯一确定替换位置。
	ErrApplyPatchAmbiguous = fmt.Errorf("%w: ambiguous anchor", ErrApplyPatch)
	// ErrApplyPatchNotFound 标记 hunk 的旧文本锚点未找到，且修复阶梯
	// 的各级都无法恢复它。
	ErrApplyPatchNotFound = fmt.Errorf("%w: anchor not found", ErrApplyPatch)
)

// applyPatchTool 实现 apply_patch：在一个补丁内对一个或多个文件执行
// add / delete / update。它是变更性工具，由 Registry.Run 在非 yolo 模式拒绝。
//
// 实现顺序：路径全部 resolve → 按 abs 排序加锁 → 锁下把所有文件读入 working
// → 在内存中演进 working（逐 hunk 用 repairReplace，add/delete 特判）→
// 全部成功后才逐个落盘。
//
// 契约：解析、路径、锚点匹配或歧义等逻辑失败保证零写入；落盘阶段若发生
// OS 错误，已写出的文件不会回滚，跨文件原子性不作承诺
// （与 multi_edit 的单文件契约一致，避免过度承诺）。
type applyPatchTool struct{}

func (applyPatchTool) Name() string { return "apply_patch" }

func (applyPatchTool) Description() string {
	return "Apply a patch to one or more files. The patch starts with *** Begin Patch and ends with *** End Patch, " +
		"and contains *** Add File / *** Delete File / *** Update File directives with @@ hunks. " +
		"Logical failures leave every file untouched."
}

func (applyPatchTool) Mutating() bool { return true }

func (applyPatchTool) Schema() string {
	return `{"type":"object","properties":{"input":{"type":"string","description":"the apply_patch text: *** Begin Patch ... *** End Patch"}},"required":["input"]}`
}

// patchWork 记录单个绝对路径文件的暂存状态。
// delete 为真时忽略 content，提交阶段执行 os.Remove。
type patchWork struct {
	abs     string // 已通过 resolve 校验的绝对路径
	display string // 模型传入的路径，用于诊断文案
	content []byte // 演进中的文件内容（add 的目标不存在时起始为空）
	exists  bool   // 磁盘上原本是否存在
	delete  bool
	added   bool
	updated bool
}

func (applyPatchTool) Run(_ context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Input string `json:"input"`
		Patch string `json:"patch"` // 兼容别名；Schema 公布的是 input
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("%w: bad arguments: %v", ErrApplyPatch, err)
	}
	patchText := args.Input
	if patchText == "" {
		patchText = args.Patch
	}
	if strings.TrimSpace(patchText) == "" {
		return "", fmt.Errorf("%w: input must not be empty", ErrApplyPatch)
	}
	actions, err := ParseApplyPatch(patchText)
	if err != nil {
		// ParseApplyPatch 的错误已包裹 ErrMalformedPatch，原样上抛。
		return "", err
	}
	if len(actions) == 0 {
		return "", fmt.Errorf("%w: no file directives in patch", ErrApplyPatch)
	}

	// ---- 阶段零：解析全部路径并校验工作区边界（尚无任何 I/O 写入）。
	// resActions 保留模型动作的原顺序，absList 去重后用于加锁与提交。
	type resAction struct {
		action PatchAction
		abs    string
	}
	resActions := make([]resAction, 0, len(actions))
	absList := make([]string, 0, len(actions))
	seenAbs := make(map[string]bool)
	for _, action := range actions {
		abs, err := resolve(root, action.Path)
		if err != nil {
			return "", fmt.Errorf("%w: resolve %q: %v", ErrApplyPatch, action.Path, err)
		}
		resActions = append(resActions, resAction{action: action, abs: abs})
		if !seenAbs[abs] {
			seenAbs[abs] = true
			absList = append(absList, abs)
		}
	}

	// ---- 阶段一：按 abs 排序获取每文件锁并持有到提交结束。
	// 排序保证并发调用以相同顺序加锁，避免加锁次序不同导致死锁。
	sort.Strings(absList)
	var unlocks []func()
	defer func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}()
	for _, abs := range absList {
		unlocks = append(unlocks, lockFileMutation(abs))
	}

	// ---- 阶段二：锁下把所有目标文件读入 working，消除 TOCTOU。
	working := make(map[string]*patchWork, len(absList))
	var order []*patchWork // 首次出现顺序，供结果文案使用
	for _, ra := range resActions {
		w := working[ra.abs]
		if w != nil {
			continue
		}
		w = &patchWork{abs: ra.abs, display: ra.action.Path}
		data, err := os.ReadFile(ra.abs)
		switch {
		case err == nil:
			w.content, w.exists = data, true
		case os.IsNotExist(err):
			w.content, w.exists = nil, false
		default:
			return "", fmt.Errorf("%w: read %s: %v", ErrApplyPatch, ra.action.Path, err)
		}
		working[ra.abs] = w
		order = append(order, w)
	}

	// ---- 阶段三：在内存中演进 working；任一步失败即整批拒绝，零写入。
	for _, ra := range resActions {
		action, abs := ra.action, ra.abs
		w := working[abs]
		switch action.Kind {
		case "update":
			if w.delete {
				return "", fmt.Errorf("%w: %s is deleted earlier in the same patch", ErrApplyPatch, action.Path)
			}
			if !w.exists {
				// 复用 patch_apply.go 的哨兵，并同时包裹 ErrApplyPatch，
				// 使调用方两种 errors.Is 判断都成立。
				return "", fmt.Errorf("%w: %w: %s", ErrApplyPatch, ErrPatchFileNotFound, action.Path)
			}
			w.updated = true
			text := string(w.content)
			for i, h := range action.Hunks {
				old := h.oldText() // 与 OldText() 等价
				newText := h.newText()
				if old == "" {
					// 纯插入 hunk 在非空文件上会被 repairReplace 判为歧义；
					// 这里直接给出更清晰的诊断，且同样安全拒绝。
					return "", fmt.Errorf("%w: %s hunk %d has no old text to anchor",
						ErrApplyPatchAmbiguous, action.Path, i+1)
				}
				out, ok := repairReplace(text, old, newText)
				if !ok {
					if strings.Count(text, old) > 1 {
						return "", fmt.Errorf("%w: %s hunk %d: old text appears %d times (not unique)",
							ErrApplyPatchAmbiguous, action.Path, i+1, strings.Count(text, old))
					}
					return "", fmt.Errorf("%w: %s hunk %d: old text not found and no repair rung matched",
						ErrApplyPatchNotFound, action.Path, i+1)
				}
				text = out // 在演进内容上应用下一个 hunk
			}
			w.content = []byte(text)

		case "add":
			if w.added || w.updated || w.delete {
				return "", fmt.Errorf("%w: duplicate action for %s in the same patch", ErrApplyPatch, action.Path)
			}
			// 校验 add 的文件体只含 '+' 或空行；其他前缀会被 NewText()
			// 静默忽略，必须显式拒绝，避免悄悄丢失模型写下的内容。
			for _, h := range action.Hunks {
				for _, ln := range h.Lines {
					if ln != "" && ln[0] != '+' {
						return "", fmt.Errorf("%w: %s add body has a non-'+' line %q",
							ErrApplyPatch, action.Path, ln)
					}
				}
			}
			// add 要求目标不存在或为空文件。
			if w.exists && len(w.content) > 0 {
				return "", fmt.Errorf("%w: %s already exists and is not empty", ErrApplyPatch, action.Path)
			}
			w.content = []byte(action.NewText())
			w.added = true

		case "delete":
			if w.delete {
				return "", fmt.Errorf("%w: duplicate delete for %s in the same patch", ErrApplyPatch, action.Path)
			}
			// delete 通常没有 hunk，OldText()/NewText() 都是空串，绝不能走
			// repairReplace（必然歧义失败）；只标记删除，落盘阶段 os.Remove。
			if !w.exists && !w.added {
				return "", fmt.Errorf("%w: %w: %s", ErrApplyPatch, ErrPatchFileNotFound, action.Path)
			}
			w.delete = true

		default:
			return "", fmt.Errorf("%w: unknown action kind %q for %s", ErrApplyPatch, action.Kind, action.Path)
		}
	}

	// ---- 阶段四：全部逻辑成功才按排序后的锁顺序提交落盘。
	for _, abs := range absList {
		w := working[abs]
		if w.delete {
			if err := os.Remove(w.abs); err != nil && !os.IsNotExist(err) {
				return "", fmt.Errorf("%w: delete %s: %v", ErrApplyPatch, w.display, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(w.abs), 0o755); err != nil {
			return "", fmt.Errorf("%w: mkdir for %s: %v", ErrApplyPatch, w.display, err)
		}
		if err := os.WriteFile(w.abs, w.content, 0o644); err != nil {
			return "", fmt.Errorf("%w: write %s: %v", ErrApplyPatch, w.display, err)
		}
	}

	names := make([]string, 0, len(order))
	for _, w := range order {
		names = append(names, w.display)
	}
	return fmt.Sprintf("applied patch to %d file(s): %s", len(order), strings.Join(names, ", ")), nil
}

// RegisterApplyPatch 把 apply_patch 工具显式追加到 r（opt-in）。它是幂等的：
// 重复调用或已存在同名工具时不再插入；已有工具顺序不变，新工具追加在末尾。
// 传入 nil 是空操作。
//
// 本函数绝不被 New/NewWithLimit 调用；只有需要该能力的调用方才会显式调用，
// 因此默认注册表的 Specs() 与 golden 快照完全一致。
func RegisterApplyPatch(r *Registry) {
	if r == nil {
		return
	}
	const name = "apply_patch"
	if _, exists := r.tools[name]; exists {
		return
	}
	r.tools[name] = applyPatchTool{}
	r.order = append(r.order, name)
}
