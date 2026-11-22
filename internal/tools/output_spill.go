package tools

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// 针对大型工具结果的集中式产出溢出，移植自 omp 审计
// （output-meta.ts）。本包提供溢出写入与恢复标记的原语；
// 实际接线在 app/output_spill_wire.go（ARGENT_OUTPUT_SPILL），
// 由工具执行循环的 clip 钩子调用 spillToolOutput。ARGENT_OUTPUT_SPILL_MAX_BYTES
// 提供总容量闸门，超限工件由 TrimSpillArtifacts 按 mtime 淘汰。

const (
	// spillMarkerOpen 标记本包插入的中段省略标记。该标记
	// 的存在意味着输出已经被溢出过，因此再次调用
	// SpillLargeOutput 是空操作（不产生重复产出，也不产生误导性的
	// 第二条恢复链接）。ASCII 标签使守卫跨语言环境保持稳定，
	// 且不受标记字节数变化的影响。
	spillMarkerOpen = "\n[output-spill: "
	spillMarkerEnd  = "]\n"

	spillArtifactPrefix = "tool-output-"
	spillArtifactSuffix = ".log"
)

// SpillLargeOutput 限制进入模型上下文的工具输出。当输出
// 超过 threshold 字节时，它把全文写入 dir 下一个新的
// 产出文件，并返回头+尾视图（中段省略），其标记命名
// 产出路径，从而被丢弃的字节仍可恢复。
//
// 失败安全：写工件是尽力而为的。写入失败时输出仍会被截断，只是没有恢复链接，
// 且 err 保持为 nil —— spill 失败绝不能把一次成功的工具调用变成错误。返回的 err
// 是保留字段且恒为 nil；调用方不得用它判断原始工具是否成功。elision 标记出现但
// artifactPath 为空，意味着写入失败，或恢复路径太长而主动省略了链接。
//
// 带有完整标记、且标记指向本包真实写出的工件的输出会被原样返回，避免二次 spill。
// 普通工具输出里仅出现标记片段不会被信任：否则任意输出都能绕过字节上限。
// 切分点始终落在 UTF-8 字形边界上，因此中文和 emoji 不会被切坏。
func SpillLargeOutput(output string, threshold int, dir string) (result string, artifactPath string, err error) {
	// 非正的 threshold 会禁用溢出；输出小于或等于阈值时
	// 已经符合上下文预算，会被原样返回。
	if threshold <= 0 || len(output) <= threshold {
		return output, "", nil
	}
	// 已经溢出过：绝不二次溢出。只信任命名了真实产出的完整标记，
	// 而非恰好出现在不可信工具输出中的子串
	// （那会完全绕过上限）。
	if isOwnSpillResult(output, dir) {
		return output, "", nil
	}

	// 先落盘再切分窗口：恢复链接里含有工件的完整路径，固定预留量覆盖不了长目录，
	// 结果会重新超过阈值。必须先拿到路径，才能按标记实际占用的字节数预留预算。
	path, writeErr := writeSpillArtifact(dir, []byte(output))
	if writeErr != nil {
		path = ""
	}

	// 用总长度充当被省略字节数的上界来估标记大小，保证预留量不小于最终标记长度；
	// 真实省略数只会更短，重建后的结果因此不会超过阈值。
	reserve := len(formatSpillMarker(len(output), path))
	if reserve >= threshold && path != "" {
		// 路径本身已占满预算：退化为不带恢复链接的标记，工件仍已落盘，
		// 只是不再通告路径，以免标记把结果撑过阈值。
		path = ""
		reserve = len(formatSpillMarker(len(output), ""))
		if reserve > threshold {
			reserve = threshold
		}
	}
	head, tail := spillWindows(output, threshold, reserve)
	omitted := len(output) - len(head) - len(tail)

	return head + formatSpillMarker(omitted, path) + tail, path, nil
}

// spillWindows 返回超长输出保留的头部与尾部切片，两端都落在 UTF-8 边界上。
// reserve 是标记（含恢复路径）占用的字节数，窗口预算必须把它扣掉，
// 否则重建后的结果会重新超过阈值。
func spillWindows(output string, threshold, reserve int) (head, tail string) {
	budget := threshold - reserve
	if budget < 0 {
		budget = 0
	}
	headBytes := budget / 2
	tailBytes := budget - headBytes

	// trimPartialRune（来自 tools_clip.go）把字节偏移回退到最近的字形起点，
	// 正好是头部窗口需要的下取整边界。
	headEnd := trimPartialRune(output, headBytes)

	// 尾部锚定在字符串末尾，因此起点要向前移动到下一个字形起点；
	// 回退会检查错误的字节，可能让后缀从半个字形开始。
	tailStart := len(output) - tailBytes
	tailStart = spillRuneCeil(output, tailStart)

	// 防御性检查：head 与 tail 不得重叠。
	if headEnd > tailStart {
		mid := trimPartialRune(output, len(output)/2)
		headEnd, tailStart = mid, spillRuneCeil(output, mid)
	}

	return output[:headEnd], output[tailStart:]
}

// spillRuneCeil 返回 >= at 且开启一个 UTF-8 rune 的最小 i。
func spillRuneCeil(s string, at int) int {
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

// spillArtifacts 扫描文本，查找 formatSpillMarker 发出的完整标记，
// 并返回它们公布的恢复路径。匹配整个标记
// （数字省略计数、链接和闭合标签）而非仅匹配其 "\n[output-spill: " 前缀
// 至关重要：仅前缀可能出现在任意工具输出中，
// 把它当作"已经溢出"会跳过溢出
// 和普通输出上限这两者。
func spillResultArtifacts(text string) []string {
	const elided = " bytes elided"
	const link = "; full output: "
	var paths []string
	for from := 0; ; {
		i := strings.Index(text[from:], spillMarkerOpen)
		if i < 0 {
			break
		}
		start := from + i
		from = start + len(spillMarkerOpen)
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
		k := strings.Index(rest, spillMarkerEnd)
		if k <= 0 {
			continue
		}
		paths = append(paths, rest[:k])
	}
	return paths
}

// spillArtifactAbsInDir 报告 p 是否为位于 dir 内、规范命名的普通
// 产出文件，有效时返回其绝对路径。这是产出路径校验的
// 唯一真实来源：幂等守卫（isOwnSpillResult）与按行
// 读取器（readSpillRangeInDir）共用它，因此伪造或越界的
// 路径不会被误认为我们的产出。校验逻辑与最初的
// 内联版本保持一致。
func spillArtifactAbsInDir(dir, p string) (string, bool) {
	if strings.TrimSpace(p) == "" {
		return "", false
	}
	if dir == "" {
		dir = "."
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	absPath, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if !spillArtifactName(filepath.Base(absPath)) {
		return "", false
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return absPath, true
}

// isOwnSpillResult 报告 text 是否为本包实际
// 溢出过的结果：某个标记必须指向已存在的、位于配置目录下的、
// 规范命名的产出。不可信工具输出中的伪造标记无法
// 满足这一点，因此此类输出会被正常限幅，而不是
// 被原样透传。
func isOwnSpillResult(text, dir string) bool {
	for _, p := range spillResultArtifacts(text) {
		if _, ok := spillArtifactAbsInDir(dir, p); ok {
			return true
		}
	}
	return false
}

// spillArtifactName 是 writeSpillArtifact 的文件名契约：固定
// 前缀、16 位小写十六进制 id 和固定后缀。它保留在
// 生产代码中（而非复用仅用于测试的正则），使幂等
// 检查不会被形似路径欺骗。
func spillArtifactName(name string) bool {
	if !strings.HasPrefix(name, spillArtifactPrefix) || !strings.HasSuffix(name, spillArtifactSuffix) {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, spillArtifactPrefix), spillArtifactSuffix)
	if len(id) != 16 {
		return false
	}
	for _, c := range id {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// formatSpillMarker 渲染中段省略标记。恢复路径
// 仅在产出写入成功时才包含。
func formatSpillMarker(omitted int, path string) string {
	if path == "" {
		return fmt.Sprintf("%s%d bytes elided%s", spillMarkerOpen, omitted, spillMarkerEnd)
	}
	return fmt.Sprintf("%s%d bytes elided; full output: %s%s", spillMarkerOpen, omitted, path, spillMarkerEnd)
}

// writeSpillArtifact 以安全的、防冲突的文件名把完整输出
// 持久化到 dir 下。os.OpenFile 配合 O_EXCL 保证永不
// 覆盖已有文件，且十六进制 id 不含路径分隔符或
// shell 元字符。它返回创建的路径或错误；调用方决定
// 如何呈现（或忽略）该错误。
func writeSpillArtifact(dir string, data []byte) (string, error) {
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// 写前容量快检：目录总字节已达上限时不再新增工件，调用方退化为不带
	// 恢复链接的截断。ARGENT_OUTPUT_SPILL_MAX_BYTES 缺省或非法时跳过检查。
	if max := spillMaxBytes(); max > 0 {
		if used, err := directoryBytes(dir); err == nil && used+int64(len(data)) > max {
			return "", ErrSpillCapacity
		}
	}

	const attempts = 8
	for i := 0; i < attempts; i++ {
		id, err := randomSpillID()
		if err != nil {
			return "", err
		}
		name := spillArtifactPrefix + id + spillArtifactSuffix
		path := filepath.Join(dir, name)

		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue // astronomically unlikely; try a fresh id
		}
		if err != nil {
			return "", err
		}

		if _, werr := f.Write(data); werr != nil {
			f.Close()
			os.Remove(path) // do not leave a partial artifact behind
			return "", werr
		}
		if cerr := f.Close(); cerr != nil {
			os.Remove(path)
			return "", cerr
		}
		return path, nil
	}
	return "", fmt.Errorf("tools: could not create a unique spill artifact in %s", dir)
}

// randomSpillID 从加密随机数生成器返回 16 位小写十六进制字符。
func randomSpillID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ---------------------------------------------------------------------------
// 接线说明（设计上不在此实现 —— 此工具是可选的）：
//
//   - 位置：在工具输出产生之后、被追加到
//     模型上下文 / 会话记录。本仓库中的入口点是
//     `clip(ctx, text)` 及其调用方（tools_builtin_bash.go、
//     tools_builtin_read.go、tools_builtin_search.go、git_status.go、glob.go、
//     repo_map.go），以及 TUI 中 internal/tui/model_mutate.go 的
//     `AppendTool`/`truncateToolOutput`。一个在这些入口调用
//     SpillLargeOutput(rawOutput, threshold, artifactDir) 的适配器即可
//     同时给模型一个有界的正文和一条恢复路径。
//   - artifactDir 约定：本代码库尚无会话产出目录，
//     目前尚未实现。候选方案是会话存储下的按会话目录，或者
//     workspace 下的 `.argent/artifacts/` 目录（见写入
//     `.argent/` 的安全说明）。这里的目录有意作为参数保留。
//   - 当 artifactPath 非 nil 时，调用方可以发出提示（"full
//     output saved to <path>"）；标记已包含路径，因此
//     单独的提示是可选的。绝不能因溢出写入失败而让工具失败。
// ---------------------------------------------------------------------------
