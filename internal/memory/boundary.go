// Package memory —— 项目根目录边界、override 文件和信任门。
//
// 本文件增加了一个可选的指令加载器，遵循 Codex 的
// 指令发现改进：向上遍历到最近的项目根目录标记，
// 从根到工作目录（根在前）收集 AGENTS.md，
// 在每个目录中优先使用 AGENTS.override.md 而非 AGENTS.md，并对
// 不受信任的项目跳过所有项目文档。现有 Load/LoadIn 行为保持不变。
// untouched.
//
// 单文件和总量上限、按 rune 安全的裁剪及截断标记
// 在此并未重新实现：本文件直接调用 memory.go 中的
// readInstructionFile 和 clipRunes（同包内），并复用 DefaultLimit、totalBudget。
// TruncationMarker.
package memory

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultMarkers 是 ProjectRoot 识别的项目根目录标记，当
// 调用方未提供自己的列表时使用。".git" 同时涵盖目录和
// worktree/submodule 指针文件；".argent" 涵盖 agent 原生项目。
var DefaultMarkers = []string{".git", ".argent"}

// LoadOptions 配置 LoadWithBoundary。零值有效：它们选择
// 默认标记、当前用户的主目录和 DefaultLimit。
type LoadOptions struct {
	// Markers 是传给 ProjectRoot 的项目根目录标记。空
	// 切片选择 DefaultMarkers。
	Markers []string
	// Home 是外层遍历边界，也是用户级文档的来源。
	// 空字符串表示 os.UserHomeDir()。测试将其设为临时目录。
	Home string
	// Limit 是单文件字节上限。值 <= 0 选择 DefaultLimit；
	// 32 KiB 总量预算始终适用。
	Limit int
}

// Instruction 是一份指令文档及其来源路径，因此
// 渲染后的系统提示可以显示溯源。它有意与 Doc 分开，
// 以使可选加载器的契约更加明确。
type Instruction struct {
	// Source 是该指令来源文件的绝对路径。
	Source string
	// Text 是文件内容，可能已按大小限制裁剪。
	Text string
	// Truncated 报告 Text 是否被单文件上限或
	// 合并后的总量预算截断。
	Truncated bool
}

// ProjectRoot 从 dir 向上遍历，返回最近的、
// 包含任一项目根目录标记的祖先目录。它同时识别 ".git"
// 目录和 ".git" 文件（worktree/submodule 指针）：只关心条目
// 是否存在。遍历停在文件系统根目录或 home 处，因此
// 用户主目录之上的标记永远不会被选中。当 markers 为空时，
// 使用 DefaultMarkers。找不到标记时返回 ""。
func ProjectRoot(dir string, markers ...string) string {
	home, _ := os.UserHomeDir()
	return projectRoot(dir, home, markers)
}

// projectRoot 是 ProjectRoot 的可测试核心：显式传入 home
// 以便测试控制上界而无需触碰真实主目录。
func projectRoot(dir, home string, markers []string) string {
	if len(markers) == 0 {
		markers = DefaultMarkers
	}
	current, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	current = filepath.Clean(current)

	stop := ""
	if home != "" {
		if abs, err := filepath.Abs(home); err == nil {
			stop = filepath.Clean(abs)
		}
	}

	for depth := 0; depth < maxDepth; depth++ {
		for _, marker := range markers {
			if marker == "" {
				continue
			}
			if _, err := os.Lstat(filepath.Join(current, marker)); err == nil {
				return current
			}
		}
		if samePath(current, stop) {
			return ""
		}
		parent := filepath.Dir(current)
		if parent == current { // filesystem root
			return ""
		}
		current = parent
	}
	return ""
}

// LoadWithBoundary 是带有项目根目录边界和信任门的可选
// 加载器。
//
// 当 trust 为 false 时，不收集任何项目文档：只返回来自
// opts.Home（为空时为 os.UserHomeDir）的用户级文档。
//
// 当 trust 为 true 时，用 ProjectRoot 找到项目根目录，
// 并从该根向下收集指令文件直到 dir（含），按
// 从根到 cwd 的顺序。每个目录中 AGENTS.override.md 优先于 AGENTS.md
// （只使用其中之一），其后是 CLAUDE.md。若未找到项目根目录，
// 则将 dir 自身作为唯一扫描目录。
//
// 文件按从 cwd 到根（最近的优先）的顺序读取并计入共享总量预算，
// 因此预算耗尽时被丢弃的是最外层、最
// 通用的指令，而最近、最具体的指令
// 得以保留。这与 Block/LoadIn 的 "nearer wins" 契约一致。
// 返回的切片仍按从根到 cwd 的顺序组装给调用方。
//
// 与 Load/LoadIn 相同的 DefaultLimit 单文件上限、32 KiB 总量预算、按 rune
// 安全的裁剪及 TruncationMarker 均适用。每条 Instruction 携带其
// Source 路径。现有 Load/LoadIn 行为不变。
func LoadWithBoundary(dir string, trust bool, opts LoadOptions) ([]Instruction, error) {
	home := opts.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	absDir = filepath.Clean(absDir)

	var dirs []string
	if !trust {
		// 不受信任：从不读取项目文件；只读取用户级文档。
		if home != "" {
			dirs = []string{filepath.Clean(home)}
		}
	} else if root := projectRoot(absDir, home, opts.Markers); root != "" {
		dirs = dirsFromRootToDir(root, absDir)
	} else {
		// 无项目根目录：只扫描工作目录。
		dirs = []string{absDir}
	}

	// dirs 是从根到 cwd。反向读取（cwd 到根），使共享的 32 KiB
	// 预算优先用于最具体的指令：预算耗尽时
	// 被丢弃的是最外层、最通用的文件。每个目录收集一组，
	// 以便结果仍能按文档记载的
	// 从根到 cwd 顺序组装，并保持目录内 AGENTS/CLAUDE 的顺序。
	var (
		perDir [][]Instruction
		used   int
	)
	for i := len(dirs) - 1; i >= 0; i-- {
		var group []Instruction
		for _, path := range dirPaths(dirs[i]) {
			var stop bool
			group, stop = appendInstruction(group, path, limit, &used)
			if stop {
				perDir = append(perDir, group)
				return assembleRootFirst(perDir), nil
			}
		}
		perDir = append(perDir, group)
	}
	return assembleRootFirst(perDir), nil
}

// assembleRootFirst 将以 cwd 到根顺序收集的每组目录
// 重新拼接为文档记载的从根到 cwd 返回顺序，保持
// 每组内的指令（AGENTS 槽在 CLAUDE.md 之前）位置不变。
func assembleRootFirst(perDir [][]Instruction) []Instruction {
	var out []Instruction
	for i := len(perDir) - 1; i >= 0; i-- {
		out = append(out, perDir[i]...)
	}
	return out
}

// dirsFromRootToDir 返回从 root 向下到 dir 的目录链，
// 含两端，按根优先顺序。它从 dir 向上遍历，一旦到达
// root（或作为安全网的文件系统根目录）即停止。
func dirsFromRootToDir(root, dir string) []string {
	var rev []string
	current := dir
	for depth := 0; depth < maxDepth; depth++ {
		rev = append(rev, current)
		if samePath(current, root) {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	dirs := make([]string, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		dirs = append(dirs, rev[i])
	}
	return dirs
}

// dirPaths 返回某个目录中要读取的指令文件，按顺序。
// 当 AGENTS.override.md 存在时取代 AGENTS.md；CLAUDE.md 始终
// 在 AGENTS 槽之后考虑。
func dirPaths(dir string) []string {
	override := filepath.Join(dir, "AGENTS.override.md")
	agents := filepath.Join(dir, "AGENTS.md")
	agentsPath := agents
	if info, err := os.Stat(override); err == nil && !info.IsDir() {
		agentsPath = override
	}
	return []string{agentsPath, filepath.Join(dir, "CLAUDE.md")}
}

// appendInstruction 用共享的单文件上限读取 path 并追加到
// out，同时执行共享总量预算。它返回更新后的切片
// 和一个停止标志，表示"预算已耗尽，调用方应停止遍历"。
// 所有裁剪/截断工作都委托给 memory.go 中的辅助函数。
func appendInstruction(out []Instruction, path string, limit int, used *int) ([]Instruction, bool) {
	body, fileTruncated, err := readInstructionFile(path, limit)
	if err != nil || body == "" {
		return out, false
	}

	budgetTruncated := false
	if limit > 0 {
		// 当单文件上限已裁剪该正文时，预留截断标记的字节；
		// 参见 LoadIn 中的相应逻辑。
		markerCost := 0
		if fileTruncated {
			markerCost = len(TruncationMarker)
		}
		remaining := totalBudget - *used
		if remaining <= markerCost {
			return out, true
		}
		if len(body)+markerCost > remaining {
			room := remaining - len(TruncationMarker)
			if room <= 0 {
				return out, true
			}
			body = clipRunes(body, room)
			budgetTruncated = true
		}
	}

	inst := Instruction{Source: path, Text: body, Truncated: fileTruncated || budgetTruncated}
	if inst.Truncated {
		inst.Text = strings.TrimRight(inst.Text, " \t\r\n") + TruncationMarker
	}
	out = append(out, inst)
	*used += len(inst.Text)
	return out, budgetTruncated || *used >= totalBudget
}

// RelevantInstructions 返回其指令目录作用域覆盖 touchedPath 的文档子集，
// 保持输入顺序。位于目录 D 中的指令文件适用于 touchedPath，当 touchedPath
// 就是 D 本身或位于 D 之下；兄弟目录或 D 的祖先子树中的规则
// 不适用。
//
// 这是 LoadWithBoundary 的路径范围伴侣：调用方不必为整个运行把每个嵌套的
// AGENTS.md/CLAUDE.md 都注入系统提示词，而是可以让完整集合常驻，只把相关的
// 切片传给 InstructionsToDocs + Block，用于工具即将触碰的文件。它适用于
// 它用于
// 按需注入，当项目有许多嵌套指令文件而每次工具调用只触碰一个子树时
// 很重要。
//
// Source 路径是绝对的，由 LoadWithBoundary 产生。touchedPath 可以是绝对路径
// （工作区解析出的工具路径的常见情况）或相对路径，相对时通过
// 此时通过 filepath.Abs 相对于进程工作目录解析。路径包含使用
// 清理后的路径，且在 Windows 上不区分大小写，与本包其他地方使用的
// withinWorkspace/samePath 一致。
//
// 该函数是纯函数：不执行磁盘 I/O，从不修改 docs，并
// 返回新切片（无匹配或输入为空时为 nil）。
func RelevantInstructions(docs []Instruction, touchedPath string) []Instruction {
	if len(docs) == 0 || strings.TrimSpace(touchedPath) == "" {
		return nil
	}

	target := filepath.Clean(touchedPath)
	if !filepath.IsAbs(target) {
		abs, err := filepath.Abs(target)
		if err != nil {
			return nil
		}
		target = abs
	}

	var out []Instruction
	for _, d := range docs {
		if d.Source == "" {
			continue
		}
		dir := filepath.Dir(filepath.Clean(d.Source))
		if withinWorkspace(target, dir) {
			out = append(out, d)
		}
	}
	return out
}

// InstructionsToDocs 将可选加载器的结果转换为 Block 接受的 Doc 类型，
// 以便 app 用现有的系统提示词块渲染它们。
func InstructionsToDocs(insts []Instruction) []Doc {
	if len(insts) == 0 {
		return nil
	}
	docs := make([]Doc, 0, len(insts))
	for _, in := range insts {
		docs = append(docs, Doc{Path: in.Source, Text: in.Text, Truncated: in.Truncated})
	}
	return docs
}
