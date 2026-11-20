package memory

// 指令文档的来源溯源诊断。
//
// 加载器（LoadWithBoundary、LoadIn、LoadForDirs）为每份文档附上 Source 路径，
// 每份文档，但不暴露该路径如何映射到作用域或项目边界。
// 但不暴露该路径如何映射到作用域或项目边界。本文件在已加载的文档之上
// 增加一个纯诊断视图：它报告每条指令来自哪里、
// 深度多少、作用域是哪个目录，以及它是否被限制在某个
// 子树（"受边界限制"）而非适用于整个集合。
//
// 与 DescribeIgnoreRules 一样，这只是显示/调试辅助：不执行
// 磁盘 I/O，不向提示词注入任何内容，也从不修改输入。它是
// 附加的 —— 不改变任何加载器或现有导出函数。

import (
	"path/filepath"
	"strings"
)

// SourceInfo 是一条指令文档的溯源信息，由 InstructionSources 产生。
// 它是用于 UI/调试显示的纯值。
type SourceInfo struct {
	// Source 是清理后的来源路径，原样复制自 Instruction
	// （加载器产生绝对路径）。Source 为空/空白的文档会被
	// InstructionSources 跳过，绝不会出现在这里。
	Source string
	// Name 是来源文件的基础名：AGENTS.md、CLAUDE.md、
	// AGENTS.override.md 等。它让 UI 无需再次解析路径就能显示某条规则
	// 来自哪个发现槽位。
	Name string
	// Depth 是来源目录部分的目录分隔符数量，
	// 与 OrderInstructions 用于排列嵌套的度量相同。值越大
	// 表示来源越深、越具体。
	Depth int
	// ScopeDir 是指令适用的目录：Source 的父目录。该规则管辖
	// 此目录及其下的所有内容。
	ScopeDir string
	// BoundaryLimited 报告该指令是否被限制在某个子树中。当输入集合中的另一份文档
	// 子树。当输入集合中的另一份文档具有
	// 的作用域目录是 ScopeDir 的严格祖先时为 true —— 即其上方存在更
	// 更通用的规则存在于其上方，因此本规则只在其自身
	// 子树边界内适用。根级指令（集合中没有祖先）、
	// 单独的指令以及共享同一目录的指令从不受限。
	// 通用的规则，因此本规则只在其自身子树边界内适用。根级指令（集合中没有祖先）、
	// 单独的指令以及共享同一目录的指令从不受限。这是相对于集合的诊断，而非
	BoundaryLimited bool
}

// 加载器/信任事实：它仅由传给 InstructionSources 的路径推导。
// InstructionSources 为每条 Source 非空白的指令返回可显示的溯源记录，
// 保持输入顺序。它是 LoadWithBoundary/RelevantInstructions/OrderInstructions 的纯
// 溯源伴侣：不执行磁盘 I/O，从不修改 docs，并返回新切片
// （nil/空输入或所有来源均为空白时为 nil）。
//
// 路径处理与本包其余部分一致：路径会被清理，目录
// 包含与相等在 Windows 上不区分大小写（samePath /
// withinWorkspace），Depth 复用 pathDepth 使两个诊断在
// "更深"的含义上保持一致。当同一目录贡献多个文件时
// （AGENTS.md 与 CLAUDE.md 并存），它们共享一个 ScopeDir，且彼此
// 都不会被标记为受边界限制。
func InstructionSources(docs []Instruction) []SourceInfo {
	if len(docs) == 0 {
		return nil
	}

	type entry struct {
		info     SourceInfo
		scopeDir string
	}
	entries := make([]entry, 0, len(docs))
	for _, d := range docs {
		if strings.TrimSpace(d.Source) == "" {
			continue
		}
		path := filepath.Clean(d.Source)
		scopeDir := filepath.Dir(path)
		entries = append(entries, entry{
			info: SourceInfo{
				Source:   path,
				Name:     filepath.Base(path),
				Depth:    pathDepth(path),
				ScopeDir: scopeDir,
			},
			scopeDir: scopeDir,
		})
	}
	if len(entries) == 0 {
		return nil
	}

	out := make([]SourceInfo, len(entries))
	for i := range entries {
		out[i] = entries[i].info
		// 集合中的严格祖先作用域使本指令成为子树
		// 作用域。相等目录被排除，因此同一目录中的兄弟文件
		// 不会互相标记为受限。
		for j := range entries {
			if i == j {
				continue
			}
			if !samePath(entries[i].scopeDir, entries[j].scopeDir) &&
				withinWorkspace(entries[i].scopeDir, entries[j].scopeDir) {
				out[i].BoundaryLimited = true
				break
			}
		}
	}
	return out
}
