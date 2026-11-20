package security

import "strings"

// ArtifactInvisibleWarning 是当声明式产物（workflow、skill、slash 命令或自动化
// 任务）携带不可见字符时发出的稳定、面向用户的警告。它是警告而不是错误：
// 解析器保留原始字节，因此解析和执行语义不变。
const ArtifactInvisibleWarning = "⚠ artifact contains invisible characters (Unicode tags or bidi controls) that may hide instructions"

// maxArtifactScanBytes 限制对一个产物所做的工作量。产物是手写的声明式文件，
// 一兆字节已远超现实大小。只检查前 1 MiB；上限之后的载荷不会被标记。
const maxArtifactScanBytes = 1 << 20

// ArtifactScanResult 是对声明式产物扫描可隐藏提示注入载荷的字符的结果。
type ArtifactScanResult struct {
	HasTags bool   // 发现了 Unicode Tag Block 字符。
	HasBidi bool   // 发现了 bidi/格式控制符。
	Warning string // 任一标志置位时为 ArtifactInvisibleWarning，否则为空。
}

// Warnings 把 Warning 作为单元素切片返回，内容干净时返回 nil。
// 它让解析器一次调用即可添加警告，无需检查空字符串。
func (r ArtifactScanResult) Warnings() []string {
	if r.Warning == "" {
		return nil
	}
	return []string{r.Warning}
}

// ScanArtifact 报告 content 中的不可见字符。它复用 invisible.go 中的检测器：
// 用 ContainsInvisibleTags 检测 Unicode Tag Block，用 isBidiOrFormat
// （EscapeBidi 转义的确切 rune 集合）检测 bidi 控制符。不修改任何内容，也不产生
// 错误：如何警告由调用方决定。
//
// 扫描限于前 maxArtifactScanBytes 字节。开头的 UTF-8 BOM 会被忽略，因为它是
// 字节序标记，而不是隐藏指令；否则每个带 BOM 前缀的 command/skill 文件都会
// 触发警告。文本中其他位置的 BOM 或任何 bidi 控制符仍会被检测到。
func ScanArtifact(content string) ArtifactScanResult {
	if len(content) > maxArtifactScanBytes {
		content = content[:maxArtifactScanBytes]
	}
	content = strings.TrimPrefix(content, "\ufeff")
	res := ArtifactScanResult{
		HasTags: ContainsInvisibleTags(content),
		HasBidi: ContainsBidi(content),
	}
	if res.HasTags || res.HasBidi {
		res.Warning = ArtifactInvisibleWarning
	}
	return res
}

// ContainsBidi 报告 s 是否包含任何 bidi/格式控制字符。它调用 invisible.go 的
// isBidiOrFormat，而不是复制其 rune 范围，因此检测永远不会与审批提示中显示的
// 转义产生偏差。
func ContainsBidi(s string) bool {
	for _, r := range s {
		if isBidiOrFormat(r) {
			return true
		}
	}
	return false
}

// RedactArtifactContent 返回 content 的显示安全副本：Unicode Tag Block 字符会被
// 移除，bidi/格式控制符会变成可见的 \uXXXX 转义。它复用 invisible.go 的
// SanitizeInvisible 和 EscapeBidi，而不是重新实现这两种转换；SanitizeInvisible
// 已经转义了 bidi，因此额外的 EscapeBidi 处理只是无害的空操作，让显示契约更明确。
//
// 这仅用于显示。它从不改变产物实际执行的内容：解析器保留原始内容，并使用
// ScanArtifact 发出警告。与 ScanArtifact 一样，只返回前 maxArtifactScanBytes
// 字节，因此调用方不会意外渲染出无界的文档。
func RedactArtifactContent(content string) string {
	if len(content) > maxArtifactScanBytes {
		content = content[:maxArtifactScanBytes]
	}
	cleaned, _ := SanitizeInvisible(content)
	return EscapeBidi(cleaned)
}
