package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

// nativeApprovalDiff 控制原生审批卡是否渲染 diff 摘要。默认 false（关闭）：
// 无论审批参数里是否带得出 diff 数据，审批卡的元素数与文案都与接线前
// 逐字节相同；只有 ARGENT_NATIVE_APPROVAL_DIFF=1 时才开启。
var nativeApprovalDiff atomic.Bool

// configureApprovalDiff 按注入的 env 设置审批 diff 显示门控。env 由调用方
// 注入而非直接读 os.Getenv，使渲染路径保持无环境副作用，且测试可精确控制
// 开关。env 为 nil 或变量不是 "1" 时关闭。
func configureApprovalDiff(env func(string) string) {
	nativeApprovalDiff.Store(env != nil && strings.TrimSpace(env("ARGENT_NATIVE_APPROVAL_DIFF")) == "1")
}

// approvalDiffVisible 报告审批卡当前是否允许渲染 diff 摘要。
func approvalDiffVisible() bool { return nativeApprovalDiff.Load() }

const (
	// approvalDiffMaxPreviewLines 约束预览的最大行数（含溢出提示行）。
	approvalDiffMaxPreviewLines = 5
	// approvalDiffMaxPathRunes 限制单行里路径的显示长度（按 rune 计）。
	// 用 []rune 截断确保 CJK/emoji 不会被切成半个字形。
	approvalDiffMaxPathRunes = 32
	// approvalDiffMaxLCSLines 与 tools 侧的 diff 计数同量级：超过该规模时
	// 回退到整文件计数，避免为审批预览分配平方级的表。
	approvalDiffMaxLCSLines = 2000
)

// approvalSummary 是一次文件编辑审批的、面向展示的差异摘要。只承载渲染
// 所需的最小信息；统计与文案生成都是纯函数，可独立测试，不依赖 UI 状态。
type approvalSummary struct {
	LinesAdded   int
	LinesRemoved int
	FilesChanged int
	Preview      string
}

// empty 报告该摘要是否为零值（没有可供渲染的数据）。
func (s approvalSummary) empty() bool {
	return s.FilesChanged == 0 && s.LinesAdded == 0 && s.LinesRemoved == 0 && s.Preview == ""
}

// summaryFileStat 是单个变更文件的统计，用于生成有界预览。
type summaryFileStat struct {
	path           string
	added, removed int
}

// buildApprovalSummary 从工具名与 JSON 参数中提取面向展示的 diff 摘要。
// 只支持变更型文件工具；其余工具（bash/read 等）与解析失败的参数都返回
// 零值摘要，调用方据此跳过渲染，使默认帧保持原样。
//
// 它是纯函数：不修改入参，不读取环境或时钟；预览路径按 rune 截断，
// CJK 文件名不会被切坏，行数始终不超过 approvalDiffMaxPreviewLines。
func buildApprovalSummary(name, args string) approvalSummary {
	switch name {
	case "write", "write_file":
		return summaryFromWrite(args)
	case "edit":
		return summaryFromEdit(args)
	case "multi_edit":
		return summaryFromMultiEdit(args)
	case "apply_patch":
		return summaryFromPatch(args)
	}
	return approvalSummary{}
}

// summaryFromWrite 处理 write 的 {"path":...,"content":...}：
// 内容作为新增行计入，删除行为 0（这里只有目标快照，没有磁盘旧内容）。
func summaryFromWrite(args string) approvalSummary {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if json.Unmarshal([]byte(args), &a) != nil || a.Path == "" {
		return approvalSummary{}
	}
	added, removed := approvalDiffLineCounts("", a.Content)
	return summaryFromStats([]summaryFileStat{{path: a.Path, added: added, removed: removed}})
}

// summaryFromEdit 处理 edit 的 old/new（含 old_text/new_text 别名与
// hashline 模式的 anchor），用有界 LCS 统计改动行数。
func summaryFromEdit(args string) approvalSummary {
	var a struct {
		Path    string `json:"path"`
		Old     string `json:"old"`
		New     string `json:"new"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
		Anchor  string `json:"anchor"`
	}
	if json.Unmarshal([]byte(args), &a) != nil || a.Path == "" {
		return approvalSummary{}
	}
	old := a.Old
	if old == "" {
		old = a.OldText
	}
	if old == "" {
		old = a.Anchor // hashline 模式下锚点即原始文本
	}
	next := a.New
	if next == "" {
		next = a.NewText
	}
	if old == "" && next == "" {
		return approvalSummary{}
	}
	added, removed := approvalDiffLineCounts(old, next)
	return summaryFromStats([]summaryFileStat{{path: a.Path, added: added, removed: removed}})
}

// summaryFromMultiEdit 处理 multi_edit 的 edits 数组：以每处编辑的 path
// （回退到顶层 path）为键聚合增删行数。edits 为空或解析失败时返回空摘要。
func summaryFromMultiEdit(args string) approvalSummary {
	var a struct {
		Path  string `json:"path"`
		Edits []struct {
			Path    string `json:"path"`
			Old     string `json:"old"`
			New     string `json:"new"`
			OldText string `json:"old_text"`
			NewText string `json:"new_text"`
		} `json:"edits"`
	}
	if json.Unmarshal([]byte(args), &a) != nil || len(a.Edits) == 0 {
		return approvalSummary{}
	}
	var stats []summaryFileStat
	index := make(map[string]int)
	for _, e := range a.Edits {
		old := e.Old
		if old == "" {
			old = e.OldText
		}
		next := e.New
		if next == "" {
			next = e.NewText
		}
		if old == "" && next == "" {
			continue
		}
		path := e.Path
		if path == "" {
			path = a.Path
		}
		i, ok := index[path]
		if !ok {
			i = len(stats)
			index[path] = i
			stats = append(stats, summaryFileStat{path: path})
		}
		added, removed := approvalDiffLineCounts(old, next)
		stats[i].added += added
		stats[i].removed += removed
	}
	return summaryFromStats(stats)
}

// summaryFromPatch 处理 apply_patch 的 input（或兼容别名 patch）文本：
// 从 *** Add/Delete/Update File 指令取路径，从 hunk 的 +/- 行取增删计数。
func summaryFromPatch(args string) approvalSummary {
	var a struct {
		Input string `json:"input"`
		Patch string `json:"patch"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return approvalSummary{}
	}
	text := a.Input
	if text == "" {
		text = a.Patch
	}
	if strings.TrimSpace(text) == "" {
		return approvalSummary{}
	}
	var stats []summaryFileStat
	index := make(map[string]int)
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		var path string
		switch {
		case strings.HasPrefix(trimmed, "*** Add File:"):
			path = strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Add File:"))
		case strings.HasPrefix(trimmed, "*** Delete File:"):
			path = strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Delete File:"))
		case strings.HasPrefix(trimmed, "*** Update File:"):
			path = strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Update File:"))
		}
		if path != "" {
			if _, ok := index[path]; !ok {
				index[path] = len(stats)
				stats = append(stats, summaryFileStat{path: path})
			}
			continue
		}
		if len(stats) == 0 {
			continue // 还没进入任何文件段
		}
		last := &stats[len(stats)-1]
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			last.added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			last.removed++
		}
	}
	return summaryFromStats(stats)
}

// summaryFromStats 聚合各文件统计并生成有界预览。stats 为空时返回零值。
func summaryFromStats(stats []summaryFileStat) approvalSummary {
	if len(stats) == 0 {
		return approvalSummary{}
	}
	s := approvalSummary{FilesChanged: len(stats)}
	for _, st := range stats {
		s.LinesAdded += st.added
		s.LinesRemoved += st.removed
	}
	s.Preview = buildApprovalPreview(stats)
	return s
}

// buildApprovalPreview 生成有界预览：每个变更文件一行 `+A/-R in path`，
// 行数不超过 approvalDiffMaxPreviewLines，超出时最后一行以省略提示收尾。
// 路径按 rune 截断，CJK 文件名不会被切坏。
func buildApprovalPreview(stats []summaryFileStat) string {
	var b strings.Builder
	for i, st := range stats {
		if i >= approvalDiffMaxPreviewLines {
			break
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		if i == approvalDiffMaxPreviewLines-1 && len(stats) > approvalDiffMaxPreviewLines {
			b.WriteString("… 还有 ")
			b.WriteString(strconv.Itoa(len(stats) - i))
			b.WriteString(" 个文件")
			break
		}
		path := st.path
		if path == "" {
			path = "?"
		}
		b.WriteByte('+')
		b.WriteString(strconv.Itoa(st.added))
		b.WriteString("/-")
		b.WriteString(strconv.Itoa(st.removed))
		b.WriteString(" in ")
		b.WriteString(truncateRunes(path, approvalDiffMaxPathRunes))
	}
	return b.String()
}

// truncateRunes 按 rune 截断到最多 max 个，保证结果始终是合法 UTF-8，
// 不会切断 CJK 汉字或 emoji 等多字节字形。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// approvalDiffLineCounts 返回两个文本之间的 (新增, 删除) 行数。
// 使用有界 LCS，使未变化的行不被计为编辑；滚动数组把空间降到 O(行数)。
func approvalDiffLineCounts(before, after string) (added, removed int) {
	a := approvalDiffSplitLines(before)
	b := approvalDiffSplitLines(after)
	if len(a) > approvalDiffMaxLCSLines || len(b) > approvalDiffMaxLCSLines ||
		len(a)*len(b) > approvalDiffMaxLCSLines*approvalDiffMaxLCSLines {
		return len(b), len(a)
	}
	prev := make([]int, len(a)+1)
	cur := make([]int, len(a)+1)
	for j := 1; j <= len(b); j++ {
		for i := 1; i <= len(a); i++ {
			if a[i-1] == b[j-1] {
				cur[i] = prev[i-1] + 1
			} else if prev[i] >= cur[i-1] {
				cur[i] = prev[i]
			} else {
				cur[i] = cur[i-1]
			}
		}
		prev, cur = cur, prev
	}
	common := prev[len(a)]
	return len(b) - common, len(a) - common
}

// approvalDiffSplitLines 按 LF 切行并规范化 CRLF，去掉末尾换行带来的空行。
func approvalDiffSplitLines(s string) []string {
	text := strings.ReplaceAll(s, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// renderApprovalSummary 生成审批卡上的稳定中文一行摘要。零值摘要返回空串，
// 调用方据此跳过渲染；非零点值始终得到同一输出，不读取时钟或环境。
func renderApprovalSummary(s approvalSummary) string {
	if s.empty() {
		return ""
	}
	return fmt.Sprintf("diff：%d 个文件，+%d/-%d 行", s.FilesChanged, s.LinesAdded, s.LinesRemoved)
}
