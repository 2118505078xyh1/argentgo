package tools

import "example.com/argentgo/internal/textutil"

// 本文件保留 tools 包原有的输出折叠 API（CollapseResult / CollapseOutput），
// 但实现已下沉到零依赖的 internal/textutil 包：tools 与 shell 都需要这个原语，
// 而 tools 已 import shell，把实现留在任一包都会形成 import 环。下沉后算法
// 只有一个定义，两个包各自做薄转发即可。
//
// CollapseResult / CollapseOutput 都是 textutil 对应类型的别名/转发，因此
// 既有调用方与测试无需改动，行为逐字节不变。

// CollapseResult 是 CollapseOutput 的结果，等价于 textutil.CollapseResult。
type CollapseResult = textutil.CollapseResult

// CollapseOutput 按行数与字符数双重预算折叠 s，语义见 textutil.CollapseOutput：
// 双约束、保留尾部错误行、插入 [N earlier lines] 标记、rune 安全、参数钳制与幂等。
func CollapseOutput(s string, maxLines, maxChars int) CollapseResult {
	return textutil.CollapseOutput(s, maxLines, maxChars)
}

// ---------------------------------------------------------------------------
// 接线说明（设计上不在此实现 —— 本原语不与现有 clipTo 相连）：
//
//   - 全文落盘与恢复路径属于外部 I/O，由调用方在拿到 CollapseOutput 的文本后，
//     复用 SpillLargeOutput(output, threshold, artifactDir) 把原始全文写入磁盘，
//     再把返回的 artifactPath 填入 CollapseResult.FullPath。本函数保持纯函数。
//   - 建议的接线位置是工具输出进入模型上下文之前（与 clip/clipTo 同层），
//     通过配置开关选择走旧的 clipTo 帧还是本原语，从而保证默认帧逐字节不变。
// ---------------------------------------------------------------------------
