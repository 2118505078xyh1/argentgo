package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

// tuiApprovalDiff 控制审批卡是否渲染真实 diff 行。默认 false（关闭），
// 因此无论审批请求里是否带有 diff 数据，弹窗帧字节都与接线前逐字节相同。
// 只有 ConfigureApprovalDiff 收到 ARGENT_APPROVAL_DIFF=1 时才开启。
var tuiApprovalDiff atomic.Bool

// ConfigureApprovalDiff 按注入的 env 设置审批 diff 显示门控。env 为 nil
// 或 ARGENT_APPROVAL_DIFF 不是 "1" 时关闭。env 由宿主注入而非直接读取
// os.Getenv，使渲染路径保持无环境副作用，且测试可以精确控制开关。
func ConfigureApprovalDiff(env func(string) string) {
	tuiApprovalDiff.Store(env != nil && strings.TrimSpace(env("ARGENT_APPROVAL_DIFF")) == "1")
}

// approvalDiffVisible 报告审批卡当前是否允许渲染 diff 行。
func approvalDiffVisible() bool { return tuiApprovalDiff.Load() }

const (
	// approvalDiffMaxPreviewLines 约束 Preview 的最大行数（含溢出提示行）。
	approvalDiffMaxPreviewLines = 4
	// approvalDiffMaxPathCells 限制单行里路径的显示宽度（按单元格计，
	// CJK 与 emoji 各占两格），避免长路径把预览撑破；Truncate 在字素
	// 边界截断，因此不会切坏多字节字形。
	approvalDiffMaxPathCells = 32
	// approvalDiffMaxLCSLines 与 tools.DiffLineCounts 同量级：超过该规模
	// 时回退到整文件计数，避免为审批预览分配平方级的表。
	approvalDiffMaxLCSLines = 4000
)

// ApprovalDiff 是一次文件编辑审批的、面向展示的差异摘要。仅承载渲染
// 所需的最小信息，不依赖 Model，也不引用 tools 的任何类型，使统计与
// 文案生成保持为可独立测试的纯函数。
type ApprovalDiff struct {
	LinesAdded   int
	LinesRemoved int
	FilesChanged int
	Preview      string
}

// empty 报告该摘要是否为零值（没有可供渲染的数据）。
func (d ApprovalDiff) empty() bool {
	return d.FilesChanged == 0 && d.LinesAdded == 0 && d.LinesRemoved == 0 && d.Preview == ""
}

// BuildApprovalDiff 对比编辑前后的文件快照，统计变更文件数与增删行数，
// 并生成有界、稳定的极简预览（每个变更文件一行，形如 `+3/-1 in foo.go`）。
//
// 它是纯函数：只读取 before/after，不修改入参；两份快照键的并集先排序
// 再处理，因此 map 的随机迭代顺序不会影响结果；预览行数不超过
// approvalDiffMaxPreviewLines，超出时最后一行以省略提示收尾。行尾 CRLF
// 被规范化，因此仅行尾变化不会夸大计数；路径经字素安全的 Truncate 处理，
// CJK 文件名不会被切坏。
func BuildApprovalDiff(before, after map[string]string) ApprovalDiff {
	var d ApprovalDiff

	// 收集两份快照键的并集；排序保证输出稳定。
	keys := make([]string, 0, len(before)+len(after))
	seen := make(map[string]bool, len(before)+len(after))
	for k := range before {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range after {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	type fileStat struct {
		path           string
		added, removed int
	}
	var changed []fileStat
	for _, path := range keys {
		old, inBefore := before[path]
		next, inAfter := after[path]
		if inBefore == inAfter && old == next {
			continue
		}
		added, removed := approvalDiffLineCounts(old, next)
		d.LinesAdded += added
		d.LinesRemoved += removed
		d.FilesChanged++
		changed = append(changed, fileStat{path: path, added: added, removed: removed})
	}

	if len(changed) > 0 {
		var b strings.Builder
		for i, c := range changed {
			if i >= approvalDiffMaxPreviewLines {
				break
			}
			if i > 0 {
				b.WriteByte('\n')
			}
			if i == approvalDiffMaxPreviewLines-1 && len(changed) > approvalDiffMaxPreviewLines {
				b.WriteString("… 还有 ")
				b.WriteString(strconv.Itoa(len(changed) - i))
				b.WriteString(" 个文件")
				break
			}
			b.WriteByte('+')
			b.WriteString(strconv.Itoa(c.added))
			b.WriteString("/-")
			b.WriteString(strconv.Itoa(c.removed))
			b.WriteString(" in ")
			b.WriteString(Truncate(c.path, approvalDiffMaxPathCells))
		}
		d.Preview = b.String()
	}
	return d
}

// approvalDiffLineCounts 返回两个文件内容之间的 (新增, 删除) 行数。
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

// RenderApprovalDiff 生成审批卡上使用的稳定中文一行摘要。零值摘要返回
// 空串，调用方据此跳过渲染；非零点值始终得到同一输出，不读取时钟或
// Model。
func RenderApprovalDiff(d ApprovalDiff) string {
	if d.empty() {
		return ""
	}
	return fmt.Sprintf("diff：%d 个文件，+%d/-%d 行", d.FilesChanged, d.LinesAdded, d.LinesRemoved)
}
