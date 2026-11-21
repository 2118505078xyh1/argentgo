package llm

import "strings"

// 本文件提供两个与 prompt cache 前缀复用相关的纯字符串原语，刻意不接线到
// 任何请求构造路径，因此默认不改变任何线上字节。它只依赖标准库。
//
// 背景：Anthropic / OpenAI 的 prompt cache 按“请求前缀的精确字节”命中。改动
// 系统提示的头部会让整段缓存失效；把变更转成“在尾部追加”则能保住前缀、直接
// 省钱。删除无法真正从前缀里抹掉（抹掉会挪动其后所有字节，等于改头），因此
// 用 tombstone 占位表达，由基础提示中的约定告知模型忽略这些占位。

// DeletionMark 作为 node 前缀时，把该 node 标记为一条删除请求。例如
// "[del]旧规则" 表示删除正文中含 "旧规则" 的既有节点。实现不会真的从 base 中
// 移除它（那会改头并破坏前缀复用），而是在尾部追加一个 tombstone。
const DeletionMark = "[del]"

// nodeSeparator 是追加 node 时使用的分隔符。换行对 ASCII 与 CJK 文本都安全，
// 且位于 base 之后，不影响缓存前缀。
const nodeSeparator = "\n"

// StableSystemPrompt 以 base 为稳定前缀构建系统提示。
//
// 语义（保证 base 的字节序列原样出现在结果开头，且绝不被重排或改写）：
//   - 普通 node 按 nodes 的顺序追加；若 base 或此前已追加的内容已包含该 node，
//     则跳过，避免重复；
//   - 带 DeletionMark 前缀的 node 表示删除请求：去掉标记后，若目标已出现在
//     已构建内容中，则追加一个 "[deleted:目标]" 占位，而不是从前缀中移除目标；
//   - 空 node 与空删除目标被忽略。
//
// 函数是纯函数：不修改 base 或 nodes，结果确定，且对 UTF-8（含 CJK）安全。
func StableSystemPrompt(base string, nodes []string) string {
	var b strings.Builder
	b.WriteString(base)

	// present 记录结果中已出现的内容片段，首个元素是 base 本身。仅做子串
	// 包含判断，与顺序无关，用 slice 即可。
	present := make([]string, 0, len(nodes)+1)
	present = append(present, base)

	for _, node := range nodes {
		if strings.HasPrefix(node, DeletionMark) {
			target := strings.TrimPrefix(node, DeletionMark)
			if target == "" || !containsString(present, target) {
				continue
			}
			b.WriteString(nodeSeparator)
			b.WriteString("[deleted:")
			b.WriteString(target)
			b.WriteString("]")
			continue
		}
		if node == "" || containsString(present, node) {
			continue
		}
		b.WriteString(nodeSeparator)
		b.WriteString(node)
		present = append(present, node)
	}
	return b.String()
}

// containsString 报告 items 中是否有任一项包含 sub。调用处已排除空 sub，
// 这里保持与 strings.Contains 一致的子串语义。
func containsString(items []string, sub string) bool {
	for _, item := range items {
		if strings.Contains(item, sub) {
			return true
		}
	}
	return false
}

// PromptPrefixShared 返回 a 与 b 的最长公共前缀长度，单位为 rune（Unicode 码点），
// 而非字节。供测试与观测两段提示的缓存前缀复用度：返回值越大，可复用的前缀越长。
// CJK 字符各算一个 rune；任一段为空则返回 0。
func PromptPrefixShared(a, b string) int {
	ar, br := []rune(a), []rune(b)
	n := len(ar)
	if len(br) < n {
		n = len(br)
	}
	i := 0
	for i < n && ar[i] == br[i] {
		i++
	}
	return i
}
