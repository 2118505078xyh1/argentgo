package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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
	// 流式计算落盘字节的 sha256 与 expected 比较，避免为 opt-in 的漂移
	// 校验把整个文件再读入内存。hashlineHash 即 sha256 的前 12 个 hex 字符，
	// 因此可直接由流式摘要得到，无需持有 actual 全文。
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(hasher, file)
	_ = file.Close()
	if copyErr != nil {
		return ""
	}
	sum := hasher.Sum(nil)
	expectedSum := sha256.Sum256(expected)
	if string(sum) == string(expectedSum[:]) {
		return ""
	}
	anchor := hex.EncodeToString(sum)[:12]
	return fmt.Sprintf("warning: the file changed after writing (possible formatter or concurrent edit); refresh your anchor with hashline %s", anchor)
}
