package session

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/textutil"
)

// pruneMarkerToken 用于在已有裁剪结果上做幂等判断：凡是已经带该标记的工具结果
// 不再二次裁剪，避免第二次调用时把"标记本身"当作可裁剪的中间部分再次压缩。
const pruneMarkerToken = "[middle pruned, "

// PruneToolResults 对超长的工具结果做压力裁剪：保留头部与尾部上下文，
// 把中间部分替换为 "… [middle pruned, N runes] …"（N 为被裁掉的 rune 数）。
//
// 行为约定：
//   - 仅处理 Role == "tool" 的结果；其他角色的 Entry 原样拷贝。
//   - 结果长度（按 rune 计）不超过 maxRunes 时不裁剪。
//   - 裁剪按 rune 切片，绝不会把一个多字节字符（CJK、emoji 等）切成两半。
//   - 头部取 maxRunes/2 个 rune，尾部取剩余的 maxRunes - maxRunes/2 个，
//     合计保留 maxRunes 个原始 rune 的前后文；N = 原长度 - 保留长度。
//   - 不修改入参：外层切片与每个 Entry 都按值拷贝，只改拷贝的 Content。
//   - 确定性：同样的入参与 maxRunes 总得到同样的输出与计数。
//   - 幂等：对已含裁剪标记的结果再次调用不会继续裁剪（count 为 0）。
//
// 返回值：pruned 是裁剪后的新切片；count 是被实际裁剪的工具结果条数。
func PruneToolResults(entries []Entry, maxRunes int) (pruned []Entry, count int) {
	pruned = make([]Entry, len(entries))
	copy(pruned, entries)

	for i := range pruned {
		entry := &pruned[i]
		if entry.Role != "tool" || entry.Content == "" {
			continue
		}
		// 已裁剪过的结果直接跳过，保证幂等。
		if strings.Contains(entry.Content, pruneMarkerToken) {
			continue
		}
		// data URL 原子替换（opt-in ARGENT_ELIDE_DATA_URLS，默认关闭）：
		// 在 rune head/tail 裁剪之前整段换掉 base64 data URL，避免切断图片引用。
		if textutil.DataURLsElisionEnabled() && strings.Contains(entry.Content, "data:") {
			entry.Content = textutil.ElideDataURLs(entry.Content)
		}
		total := utf8.RuneCountInString(entry.Content)
		if total <= maxRunes {
			continue
		}

		// maxRunes 非正时 head/tail 都为 0，仅保留标记与 N；不影响 rune 安全。
		head, tail := 0, 0
		if maxRunes > 0 {
			head = maxRunes / 2
			tail = maxRunes - head
		}

		runes := []rune(entry.Content)
		removed := total - head - tail
		entry.Content = string(runes[:head]) + pruneMarkerText(removed) + string(runes[total-tail:])
		count++
	}
	return pruned, count
}

// pruneMarkerText 生成中间裁剪标记；n 为被裁掉的 rune 数。
func pruneMarkerText(n int) string {
	return "… [middle pruned, " + strconv.Itoa(n) + " runes] …"
}
