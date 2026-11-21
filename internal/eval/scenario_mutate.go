package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// 本文件提供 ratchet eval 爬山优化蓝图所需的确定性场景变体工具。
// 三个函数都是纯函数：不修改入参、不依赖时钟/网络/全局可变状态，
// 不读取文件或环境，也不调用现有 golden/评分路径。目前没有调用方。

// MutatePrompt 根据 mode 返回 prompt 的一个确定性变体。
//
// mode 取值：
//   - "trim"：去掉首尾空白，并把任意连续空白（空格、制表、换行、全角空格等）
//     折叠为单个 ASCII 空格；seed 不影响结果。
//   - "upper-wrap"：用一层大写的、含 seed 短哈希的标记包裹 prompt。
//     prompt 的语义词（标记之间的原文）逐字节保留，不改大小写、不裁剪；
//     seed 决定标记中的哈希串，因此同一种子输出稳定、不同种子输出不同。
//   - "none"：原样返回 prompt。未知 mode 也按 "none" 处理。
//
// 同样的 (prompt, seed, mode) 永远得到同样的输出。
func MutatePrompt(prompt, seed, mode string) string {
	switch mode {
	case "trim":
		// Fields 按 Unicode 空白切分并丢弃空串，Join 用单空格重新连接，
		// 一步完成「去首尾 + 折叠连续空白」。
		return strings.Join(strings.Fields(prompt), " ")
	case "upper-wrap":
		h := sha256.Sum256([]byte(seed))
		tag := hex.EncodeToString(h[:])[:8]
		return fmt.Sprintf("<<<VARIANT hash=%s>>>\n%s\n<<<END_VARIANT hash=%s>>>", tag, prompt, tag)
	default: // "none" 与未知 mode
		return prompt
	}
}

// ScenarioFingerprint 返回场景的稳定 SHA-256 指纹（64 位小写十六进制）。
//
// name 与 prompt 以「十进制长度:内容」的形式拼接后再哈希，长度前缀消除了
// 字段边界歧义：例如 ("ab", "c") 与 ("a", "bc") 会得到不同的指纹。
// 纯函数，同样的输入永远得到同样的输出，适合跨进程、跨版本比对。
func ScenarioFingerprint(name, prompt string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s%d:%s", len(name), name, len(prompt), prompt)
	return hex.EncodeToString(h.Sum(nil))
}

// SelectVariants 从 names 中选出前 n 个名字。
//
// 排序键是 sha256(seed + "\x00" + name)，按键的字节序升序排序后取前 n：
//   - 同样的 (names, seed, n) 输出确定；
//   - seed 改变会改变排序键，从而改变选择结果（种子敏感）；
//   - 排序键相同时按名字字典序兜底，使结果与 names 的传入顺序无关。
//
// 边界：n <= 0 或 names 为空时返回非 nil 的空切片；n 超过长度时返回全部。
// 函数不修改入参 names，返回的是新切片。
func SelectVariants(names []string, seed string, n int) []string {
	if n <= 0 || len(names) == 0 {
		return []string{}
	}

	keyed := make([]struct {
		name string
		key  [sha256.Size]byte
	}, len(names))
	for i, name := range names {
		keyed[i].name = name
		keyed[i].key = sha256.Sum256([]byte(seed + "\x00" + name))
	}

	sort.Slice(keyed, func(i, j int) bool {
		if c := bytes.Compare(keyed[i].key[:], keyed[j].key[:]); c != 0 {
			return c < 0
		}
		return keyed[i].name < keyed[j].name
	})

	if n > len(keyed) {
		n = len(keyed)
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = keyed[i].name
	}
	return out
}
