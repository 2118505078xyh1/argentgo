package mcp

import (
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// outputLimitEnv 是 MCP 输出硬上限的独立门控（字节）；缺省 0 保持旧行为。
const outputLimitEnv = "ARGENT_MCP_OUTPUT_LIMIT_BYTES"

func mcpOutputLimit() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(outputLimitEnv)))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// limitMCPOutput 在 opt-in 开启且输出超过 limit 时保留头尾并插入带原始字节/行数
// 的恢复标记。切分落在 UTF-8 边界；原文字节在窗口内原样保留。
// limit<=0 时原样返回，逐字节兼容默认路径。
func limitMCPOutput(text string, limit int) string {
	if limit <= 0 || len(text) <= limit || !utf8.ValidString(text) {
		return text
	}
	head := limit / 2
	tail := limit - head
	// 把切点吸附到 rune 边界。
	for head > 0 && !utf8.RuneStart(text[head]) {
		head--
	}
	for tail > 0 && !utf8.RuneStart(text[len(text)-tail]) {
		tail--
	}
	lines := strings.Count(text, "\n") + 1
	marker := "\n[full output: " + strconv.Itoa(len(text)) + " bytes, " + strconv.Itoa(lines) + " lines; truncated to " + strconv.Itoa(limit) + " bytes]\n"
	return text[:head] + marker + text[len(text)-tail:]
}
