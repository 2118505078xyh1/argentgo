package cli

import (
	"strconv"
	"strings"
)

// describeConfigSources 把已合并的配置来源渲染成一段可读的"来源链"文本，
// 用于定位某个键可能被哪一层配置贡献。sources 的顺序即优先级由低到高，
// 输出严格按该顺序排列，不排序也不修改入参，因此结果稳定可比较。
//
// 该函数是纯函数：不读文件、不读环境、不产生副作用。key 与每一层路径都
// 以完整字符串写入，不做任何字节切片，因此中文等多字节字符不会被截断；
// 没有来源时返回空串。
func describeConfigSources(sources []string, key string) string {
	if len(sources) == 0 {
		return ""
	}

	var b strings.Builder
	// 表头点明被追踪的键与层数，便于在输出中一眼识别这段来源链。
	b.WriteString("配置来源链 [")
	b.WriteString(key)
	b.WriteString("]（优先级由低到高，共 ")
	b.WriteString(strconv.Itoa(len(sources)))
	b.WriteString(" 层）:")
	for i, source := range sources {
		b.WriteString("\n  ")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(key)
		b.WriteString(" 来自 ")
		b.WriteString(source)
	}
	return b.String()
}
