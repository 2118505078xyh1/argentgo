package cli

import (
	"sort"
	"strings"
)

// resolveSessionPrefix 把用户输入的会话短 ID / 前缀解析为 ids 中的真实会话 ID。
// 它是纯函数：只读参数，不做 I/O、不访问配置/会话存储/全局状态，结果仅由入参决定，
// 因此既能安全地用于表驱动测试，也能在将来由会话子命令 opt-in 调用，而不改变当前
// 任何命令的字节级输出（当前尚未接线）。
//
// 会话 ID 形如「20261120-120000-a1b2」，用户往往只输入前几位。解析规则按优先级：
//  1. 字节级精确命中：input 与某 ID 逐字节相同，直接返回该 ID；
//  2. 大小写不敏感的精确命中：按 rune 折叠后相等，返回对应的真实 ID，避免某个
//     短 ID 同时是另一个 ID 的前缀时被误报为歧义；
//  3. 前缀匹配：折叠后以 input 开头的 ID 构成候选集——
//     - 唯一命中：返回该 ID，candidates 为 nil；
//     - 多命中：match 为空，candidates 为按字典序排序的真实 ID 列表；
//  4. 无命中（含空输入、空 ID 集）：match 为空，candidates 为 nil。
//
// 大小写折叠复用 foldCommandPrefix：它逐 rune 用 unicode.ToLower 写回，不切分多字节
// 的 CJK/emoji 字符，也不依赖 strings.ToLower 的区域规则，因此对中英混排 ID 安全且
// 跨调用稳定。返回值均为 ids 中的原始字符串，不是折叠后的形式。
//
// candidates 只包含裸 ID，不带创建日期。歧义提示里的「创建日期占位」由调用方在展示
// 时按 ID 补充，从而让本函数保持纯粹、便于复用与测试。候选会去重，且顺序与入参顺序
// 无关（先按出现顺序收集，再按字典序排序）。函数不会修改传入的切片。
func resolveSessionPrefix(input string, ids []string) (match string, candidates []string) {
	if input == "" || len(ids) == 0 {
		return "", nil
	}

	// 阶段 1：字节级精确命中，保留调用方写入的原样。
	for _, id := range ids {
		if id == input {
			return id, nil
		}
	}

	foldedInput := foldCommandPrefix(input)

	// 阶段 2：大小写不敏感的精确命中。
	for _, id := range ids {
		if foldCommandPrefix(id) == foldedInput {
			return id, nil
		}
	}

	// 阶段 3：前缀匹配，收集并去重候选。
	seen := make(map[string]struct{})
	var hits []string
	for _, id := range ids {
		if !strings.HasPrefix(foldCommandPrefix(id), foldedInput) {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		hits = append(hits, id)
	}

	switch len(hits) {
	case 0:
		return "", nil
	case 1:
		return hits[0], nil
	default:
		sort.Strings(hits)
		return "", hits
	}
}
