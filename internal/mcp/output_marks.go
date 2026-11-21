package mcp

import (
	"strconv"
	"strings"
)

// 本文件定义一组自研的 shell 输出标记原语，思路来自 dsh 的
// [timed out] / [killed by signal] / [exit code] 约定：把一次进程输出的
// 终止与裁剪事实编码成稳定、可解析的短标记行，便于模型与上层快速判断
// 调用究竟是正常结束、超时、被信号杀死，还是输出被截断/溢出到产物。
//
// 设计约束：
//   - 仅用标准库；零 I/O、零调用方，刻意不接入现有调用流程。
//   - RenderMarks / ParseMarks 均为纯函数：相同输入恒定返回相同输出，
//     不修改入参、不持有全局状态。
//   - 标记按固定顺序渲染：退出码 → 超时 → 信号 → 截断 → 落盘路径，
//     与 map 遍历顺序无关，因此逐字节稳定。
//   - 切分只发生在 ASCII 标记边界（'[' / ']' 为单字节，且不会出现在 UTF-8
//     续字节中），因此含 CJK 的路径/信号不会被切坏。

// OutputMarks 是一次输出的关键终止与裁剪事实。字段均为值类型，
// 零值表示“该标记不存在”。
type OutputMarks struct {
	ExitCode  int    // 退出码；0 表示正常结束，不渲染标记
	TimedOut  bool   // 是否超时
	Signal    string // 终止信号名（如 SIGKILL）；空串表示未被信号杀死
	Truncated bool   // 输出是否被截断
	SpillPath string // 完整输出落盘路径；空串表示未溢出
}

// 渲染用的稳定标记片段。关键字保持英文，与现有 [output-spill: ...] 约定一致，
// 便于模型与恢复工具复用同一套识别习惯；各行以换行分隔。
const (
	marksExitPrefix    = "[exit code: "
	marksTimeout       = "[timed out]"
	marksTimeoutPrefix = "[timed out after"
	marksSignalPrefix  = "[killed by signal: "
	marksTruncated     = "[truncated]"
	marksSpillPrefix   = "[full output: "
	marksClose         = "]"
)

// RenderMarks 把 m 渲染为按稳定顺序排列的标记行，以换行分隔。
// 全零值返回空串。该函数是纯函数，不修改 m。
func RenderMarks(m OutputMarks) string {
	var lines []string
	if m.ExitCode != 0 {
		lines = append(lines, marksExitPrefix+strconv.Itoa(m.ExitCode)+marksClose)
	}
	if m.TimedOut {
		lines = append(lines, marksTimeout)
	}
	if m.Signal != "" {
		lines = append(lines, marksSignalPrefix+m.Signal+marksClose)
	}
	if m.Truncated {
		lines = append(lines, marksTruncated)
	}
	if m.SpillPath != "" {
		lines = append(lines, marksSpillPrefix+m.SpillPath+marksClose)
	}
	return strings.Join(lines, "\n")
}

// ParseMarks 从文本中尽力解析出标记。它扫描全文寻找已知标记，忽略无法
// 识别或畸形的片段；同一标记多次出现时取第一个合法值。该函数是纯函数，
// 不修改 s。
func ParseMarks(s string) OutputMarks {
	return OutputMarks{
		ExitCode:  parseMarksExitCode(s),
		TimedOut:  parseMarksTimedOut(s),
		Signal:    parseMarksSignal(s),
		Truncated: strings.Contains(s, marksTruncated),
		SpillPath: parseMarksSpillPath(s),
	}
}

// parseMarksExitCode 解析退出码标记，畸形或越界的值按零处理。
func parseMarksExitCode(s string) int {
	v, ok := findMarkValue(s, marksExitPrefix)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// parseMarksTimedOut 识别超时标记，同时容忍 [timed out] 与带时长的
// [timed out after ...] 两种写法。
func parseMarksTimedOut(s string) bool {
	return strings.Contains(s, marksTimeout) || strings.Contains(s, marksTimeoutPrefix)
}

// parseMarksSignal 解析信号标记。
func parseMarksSignal(s string) string {
	v, _ := findMarkValue(s, marksSignalPrefix)
	return v
}

// parseMarksSpillPath 解析落盘路径标记。
func parseMarksSpillPath(s string) string {
	v, _ := findMarkValue(s, marksSpillPrefix)
	return v
}

// findMarkValue 在 s 中寻找 prefix 后的第一个 ']'，返回两者之间去空白的
// 内容。只在 ASCII 标记边界切分，保证 CJK 内容不会被切在字符中间；
// 找不到闭合括号时返回 false，实现容错。
func findMarkValue(s, prefix string) (string, bool) {
	i := strings.Index(s, prefix)
	if i < 0 {
		return "", false
	}
	start := i + len(prefix)
	j := strings.Index(s[start:], marksClose)
	if j < 0 {
		return "", false
	}
	return strings.TrimSpace(s[start : start+j]), true
}
