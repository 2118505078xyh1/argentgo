package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 本文件在非常大的 MCP 工具输出进入模型上下文之前对其进行限制。
// goose 审计发现 CallTool 会逐字拼接每个 text/resource 块，
// 因此单个话痨服务器就能淹没上下文；内置工具早已把超大输出
// 溢出到产物（internal/tools/output_spill*.go），这里把同样的
// 上限 + 恢复链接带到 MCP。
//
// 设计约束（仿照 blob.go）：
//   - 默认行为逐字节不变。只有通过 ARGENT_MCP_SPILL_DIR 显式配置了产物
//     目录时才进行溢出（CallTool 还会读取 ARGENT_MCP_SPILL_THRESHOLD）。
//     两者都未设置时，spillMCPOutput 是空操作，
//     CallTool 保持其旧代码路径。
//   - 仅用标准库；产物约定（0700 目录、0600 O_EXCL 文件、随机十六进制
//     名称、尽力而为且绝不让原调用失败的写入）复用自 SaveBlobArtifact，
//     而不导入 internal/tools。
//   - 标记格式与 internal/tools 的 SpillLargeOutput 完全相同，因此模型和
//     任何恢复工具都看到一致的约定：
//     "\n[output-spill: N bytes elided; full output: PATH]\n"。
//   - 切分总是落在 UTF-8 字符边界上，因此 CJK/emoji 绝不会被拆开。

const (
	// mcpSpillDirEnv 让 CallTool 的输出溢出选择写入产物。为空
	// 或未设置保持旧行为（完全不溢出）。
	mcpSpillDirEnv = "ARGENT_MCP_SPILL_DIR"
	// mcpSpillThresholdEnv 可选地覆盖 CallTool 使用的字节阈值。
	// 非正值或无法解析的值会选择默认值。
	mcpSpillThresholdEnv = "ARGENT_MCP_SPILL_THRESHOLD"
	// defaultMCPSpillThreshold 与 tools.DefaultOutputSpillThreshold 一致：远高于
	// 普通裁剪上限，因此只有真正巨大的输出才会溢出。
	defaultMCPSpillThreshold = 200 << 10 // 200 KiB

	// 标记字符串从 internal/tools/output_spill.go 逐字复制，使两个包发出
	// 完全一致、可识别的恢复标记。
	mcpSpillMarkerOpen    = "\n[output-spill: "
	mcpSpillMarkerEnd     = "]\n"
	mcpSpillMarkerReserve = 256 // 为标记 + 恢复路径在预算中留出空间
)

// mcpSpillDir 报告配置的产物目录。为空表示溢出
// 禁用，spillMCPOutput 和 CallTool 都保持旧行为。
func mcpSpillDir() string {
	return strings.TrimSpace(os.Getenv(mcpSpillDirEnv))
}

// mcpSpillThreshold 返回 CallTool 用于溢出的字节阈值。它
// 从环境读取，使集成无需重新构建即可调整；默认值
// 使普通 MCP 回复保持不变。
func mcpSpillThreshold() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(mcpSpillThresholdEnv)))
	if err != nil || n <= 0 {
		return defaultMCPSpillThreshold
	}
	return n
}

// spillMCPOutput 为模型上下文限制一次 MCP 工具结果。当文本长于
// 阈值字节且配置了产物目录时，它把完整文本写入该目录下的
// 一新产物，并返回一个 head+tail 视图（中间省略），
// 携带恢复路径标记。否则原样返回文本且 spilled == false。
// 它绝不会让原工具调用失败：写入失败时文本
//
// 仍会被限制，但标记省略恢复路径且 artifactPath 为空。
// 切分落在 UTF-8 字符边界上，因此 CJK 文本和 emoji
// 保持完整。
func spillMCPOutput(text string, threshold int) (result string, artifactPath string, spilled bool) {
	// 非正阈值和输出在阈值及以下都无需处理。
	if threshold <= 0 || len(text) <= threshold {
		return text, "", false
	}
	dir := mcpSpillDir()
	if dir == "" {
		// 没有选择加入的目录：旧行为，绝不溢出。
		return text, "", false
	}
	// 完整的标记指向我们某个真实产物，说明这段文本已被
	// 溢出过；不要第二次溢出（或截断）它。
	if isMCPSpillResult(text, dir) {
		return text, "", false
	}

	head, tail := mcpSpillWindows(text, threshold)
	omitted := len(text) - len(head) - len(tail)

	// 尽力而为的持久化：失败只会丢失恢复链接。
	path, err := SaveBlobArtifact(dir, []byte(text), ".txt")
	if err != nil {
		path = ""
	}
	return head + formatMCPSpillMarker(omitted, path) + tail, path, true
}

// mcpSpillWindows 返回从超大结果中保留的、字符安全的 head 和 tail 切片。
// 预留空间使标记和恢复路径不会把重建后的结果再次推过
// 阈值。
func mcpSpillWindows(text string, threshold int) (head, tail string) {
	budget := threshold - mcpSpillMarkerReserve
	if budget <= 0 {
		budget = threshold
	}
	headBytes := budget / 2
	tailBytes := budget - headBytes

	headEnd := mcpRuneFloor(text, headBytes)
	tailStart := mcpRuneCeil(text, len(text)-tailBytes)
	if headEnd > tailStart {
		mid := mcpRuneFloor(text, len(text)/2)
		headEnd, tailStart = mid, mcpRuneCeil(text, mid)
	}
	return text[:headEnd], text[tailStart:]
}

// mcpRuneFloor 返回 <= at 且是一个 UTF-8 字符起始位置的最大 i。
func mcpRuneFloor(s string, at int) int {
	if at <= 0 {
		return 0
	}
	if at >= len(s) {
		return len(s)
	}
	i := at
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// mcpRuneCeil 返回 >= at 且是一个 UTF-8 字符起始位置的最小 i。
func mcpRuneCeil(s string, at int) int {
	if at <= 0 {
		return 0
	}
	if at >= len(s) {
		return len(s)
	}
	i := at
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// formatMCPSpillMarker 渲染中间省略标记。恢复路径仅当
// 产物写入成功时才包含。
func formatMCPSpillMarker(omitted int, path string) string {
	if path == "" {
		return fmt.Sprintf("%s%d bytes elided%s", mcpSpillMarkerOpen, omitted, mcpSpillMarkerEnd)
	}
	return fmt.Sprintf("%s%d bytes elided; full output: %s%s", mcpSpillMarkerOpen, omitted, path, mcpSpillMarkerEnd)
}

// isMCPSpillResult 报告 text 是否是本功能实际溢出过的结果：
// 必须有某个完整标记指向 dir 下存在且规范命名的产物。
// 不可信服务器输出中伪造的标记子串*不*被信任，因此这类输出
// 会被正常限制，而不是直接通过。
func isMCPSpillResult(text, dir string) bool {
	const (
		elided = " bytes elided"
		link   = "; full output: "
	)
	for from := 0; ; {
		i := strings.Index(text[from:], mcpSpillMarkerOpen)
		if i < 0 {
			return false
		}
		start := from + i
		from = start + len(mcpSpillMarkerOpen)
		rest := text[from:]

		j := strings.Index(rest, elided)
		if j <= 0 {
			continue
		}
		digits := rest[:j]
		numeric := true
		for _, c := range digits {
			if c < '0' || c > '9' {
				numeric = false
				break
			}
		}
		if !numeric {
			continue
		}
		rest = rest[j+len(elided):]
		if !strings.HasPrefix(rest, link) {
			continue
		}
		rest = rest[len(link):]
		k := strings.Index(rest, mcpSpillMarkerEnd)
		if k <= 0 {
			continue
		}
		if _, ok := mcpArtifactAbsInDir(dir, rest[:k]); ok {
			return true
		}
	}
}

// mcpArtifactAbsInDir 把 p 校验为由 SaveBlobArtifact 在 dir 下产生的
// 规范产物，有效时返回其绝对路径。
func mcpArtifactAbsInDir(dir, p string) (string, bool) {
	if strings.TrimSpace(p) == "" || strings.TrimSpace(dir) == "" {
		return "", false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	absPath, err := filepath.Abs(p)
	if err != nil || !pathWithinDir(absDir, absPath) {
		return "", false
	}
	if !mcpArtifactName(filepath.Base(absPath)) {
		return "", false
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return absPath, true
}

// mcpArtifactName 检查 SaveBlobArtifact 的文件名约定：固定的
// blob 前缀、16 位小写十六进制 id，以及可选的安全扩展名。
func mcpArtifactName(name string) bool {
	if !strings.HasPrefix(name, blobArtifactPrefix) {
		return false
	}
	rest := strings.TrimPrefix(name, blobArtifactPrefix)
	id := rest
	if dot := strings.IndexByte(rest, '.'); dot >= 0 {
		id = rest[:dot]
		ext := rest[dot+1:]
		if ext == "" || len(ext) > maxArtifactExtLen {
			return false
		}
		for _, c := range ext {
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z') {
				return false
			}
		}
	}
	if len(id) != blobIDBytes*2 {
		return false
	}
	for _, c := range id {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}
