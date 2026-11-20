package tools

import (
	"fmt"
	"strings"
)

// FormatHook 是可选的落盘前内容转换：接收磁盘上的目标路径与即将写入的
// 字节，返回格式化后的字节。返回空切片或与原内容完全相同的字节表示
// 无需格式化。钩子只做纯转换，不触碰文件；路径解析、变更锁与写入仍由
// 工具本身负责。
type FormatHook func(path string, content []byte) []byte

// formatDiffSummaryLimit 约束 formatAwareDiff 结果的最大字节数，使摘要
// 不随文件内容增长而无限膨胀，并借助 clipTo 的边界处理保证合法 UTF-8。
const formatDiffSummaryLimit = 64

// formattedMarker 标记最终落盘内容已被格式化钩子改写。
const formattedMarker = "（已格式化）"

// formatAwareDiff 生成"编辑后可选格式化"落盘内容相对 before 的简短行数差异摘要。
//
// 参数：
//   - before：编辑前的文件内容；
//   - after：编辑写入的内容（格式化之前）；
//   - formatted：格式化后的内容，空串表示没有可用的格式化结果。
//
// 仅当 formatted 非空且与 after 不同时，才以 formatted 作为最终内容，
// 否则以 after 为准。这样摘要始终反映真正落盘的字节，模型看到的内容
// 与磁盘保持一致。返回值形如 "+3/-1 行"；格式化确实改变内容时追加
// "（已格式化）"。输出有界且为合法 UTF-8。
func formatAwareDiff(before, after, formatted string) string {
	final := after
	applied := false
	if formatted != "" && formatted != after {
		final = formatted
		applied = true
	}
	added, removed := DiffLineCounts([]byte(before), []byte(final))

	var b strings.Builder
	fmt.Fprintf(&b, "+%d/-%d 行", added, removed)
	if applied {
		b.WriteString(formattedMarker)
	}
	return clipTo(b.String(), formatDiffSummaryLimit)
}
