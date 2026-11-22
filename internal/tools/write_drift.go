package tools

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

// writeDriftEnv 是「写后漂移校验」的独立门控变量。
const writeDriftEnv = "ARGENT_VERIFY_WRITE"

func writeDriftEnabled() bool {
	return strings.TrimSpace(os.Getenv(writeDriftEnv)) == "1"
}

// verifyWriteDrift 在 opt-in 开启时于写入后重新读取目标文件，与预期字节比较。
// 若不一致（格式化钩子竞争、外部编辑器保存等），返回可读警告并附按落盘字节
// 重算的 hashline 锚点，使后续 anchor/hashline 编辑基于真实快照。
// 默认关闭时零额外 syscall；一致时返回空串。
func verifyWriteDrift(path string, expected []byte) string {
	if !writeDriftEnabled() {
		return ""
	}
	actual, err := os.ReadFile(path)
	if err != nil || bytes.Equal(actual, expected) {
		return ""
	}
	return fmt.Sprintf("warning: the file changed after writing (possible formatter or concurrent edit); refresh your anchor with hashline %s", hashlineHash(string(actual)))
}
