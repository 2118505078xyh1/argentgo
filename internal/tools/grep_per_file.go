package tools

import (
	"os"
	"strconv"
	"strings"
)

// grepPerFileEnv 是「grep 单文件匹配上限」的独立门控变量。
// 默认未设置或 0 时上限禁用，两个搜索入口的行为逐字节不变。
const grepPerFileEnv = "ARGENT_GREP_PER_FILE"

// grepPerFileLimit 返回配置的单文件匹配上限；<=0 表示禁用。
// 非法值视为 0（禁用），与工具层其它 env 解析的宽容风格一致。
func grepPerFileLimit() int {
	value := strings.TrimSpace(os.Getenv(grepPerFileEnv))
	if value == "" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
