package llm

import "strings"

// 本文件提供"不可信边界诊断"原语：不修改文本、不触碰任何 wire 协议、
// 不改变请求行为，只扫描一段文本并报告它当前处于何种"已标记"状态。
// 它是 MarkUntrusted / MarkToolBoundary 的只读观测面，供调用方判断某段
// 模型可见文本是否已被正确加固。
//
// 与 untrusted_wire.go 的 BoundaryStats 一致：标记与转义字符均为 ASCII，
// 按字节扫描不会破坏 CJK / emoji 等多字节 UTF-8 序列；纯函数、只读入参、
// 仅依赖标准库。

// BoundaryReport 是一次边界诊断的结果。
//
//   - Marked：是否至少出现一个未被转义的真实边界（开或闭）。
//   - OpenCount / CloseCount：未被转义的真实开 / 闭边界数量。
//   - EscapedCount：被转义的边界标记数量（\<untrusted_content> 与
//     \</untrusted_content>）；字面转义字符（\\）与孤立转义字符不计入。
//   - Balanced：是否恰好一层包裹，即 OpenCount == 1 且 CloseCount == 1。
type BoundaryReport struct {
	Marked       bool
	OpenCount    int
	CloseCount   int
	EscapedCount int
	Balanced     bool
}

// DiagnoseBoundary 扫描 s 并报告其中不可信边界的状态。
//
// 计数规则与 escapeUntrusted / unescapeUntrusted / BoundaryStats 完全一致：
//   - 转义字符 + 边界标记视为转义边界，计入 EscapedCount，不算真实边界；
//   - 两个连续转义字符（\\）是一个字面转义字符，跳过两字节，不计入任何计数；
//   - 其余直接出现的边界标记才是真实边界，分别计入 OpenCount / CloseCount；
//   - 孤立的转义字符（如结尾的单个 \）原样跳过，不计入计数。
//
// Balanced 采用严格的"单层包裹"定义：真实开、闭边界各恰好一个。因此完整的
// MarkUntrusted 结果为 Balanced=true；并排的两对真实边界（2/2）Balanced=false；
// 双重包裹由于内层被转义，仍只有一对真实边界，Balanced=true。
//
// 该函数是纯函数：只读 s、不修改入参、无副作用；CJK / emoji 等字符原样跳过。
func DiagnoseBoundary(s string) BoundaryReport {
	var open, closed, escaped int
	for i := 0; i < len(s); {
		if s[i] == untrustedEscape && i+1 < len(s) {
			next := s[i+1:]
			switch {
			case strings.HasPrefix(next, UntrustedOpen):
				escaped++
				i += 1 + len(UntrustedOpen)
				continue
			case strings.HasPrefix(next, UntrustedClose):
				escaped++
				i += 1 + len(UntrustedClose)
				continue
			case next[0] == untrustedEscape:
				// 字面转义字符：跳过一对，不计入边界。
				i += 2
				continue
			}
		}
		if strings.HasPrefix(s[i:], UntrustedOpen) {
			open++
			i += len(UntrustedOpen)
			continue
		}
		if strings.HasPrefix(s[i:], UntrustedClose) {
			closed++
			i += len(UntrustedClose)
			continue
		}
		i++
	}
	return BoundaryReport{
		Marked:       open > 0 || closed > 0,
		OpenCount:    open,
		CloseCount:   closed,
		EscapedCount: escaped,
		Balanced:     open == 1 && closed == 1,
	}
}

// LooksUntrusted 报告 s 中是否存在未被转义的真实边界，即它是否"看起来"
// 已经被 MarkUntrusted / MarkToolBoundary 标记过。
//
// 它等价于 DiagnoseBoundary(s).Marked，比 Balanced 更宽松：只要有一个真实
// 边界即为 true，即使只有开标记、只有闭标记或出现多对边界。被转义的伪造边界
// 不算真实边界，因此仅含 \<...> / \</...> 的纯文本返回 false。
//
// 该函数是纯函数、只读入参。
func LooksUntrusted(s string) bool {
	return DiagnoseBoundary(s).Marked
}
