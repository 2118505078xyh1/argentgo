package memory

import (
	"path/filepath"
	"sort"
	"strings"
)

// OrderInstructions 返回为级联式注入重新排序的 docs：
// 按每个 Source 路径的目录深度从根到 cwd 排列，因此指令
// 越靠近工作目录越靠后，在"后者优先"的
// 渲染器下覆盖其上更通用的规则。
//
// 它是 LoadWithBoundary/RelevantInstructions 的纯排序伴侣：
// 溯源伴侣：不执行磁盘 I/O，从不修改 docs，并返回新切片
// 切片）。排序规则如下：
//
//   - 按目录深度排序：Source 目录越深越靠近 cwd，
//     位置越靠后。深度是清理后目录部分中的分隔符数量，
//     因此只要所有来源共享一个工作区根目录（加载器产生的 docs 就是如此），
//     排名就能反映真实的嵌套层次。
//   - 同深度的条目按其规范化 Source 路径文本排序，
//     并以稳定的决胜项打破平局，保证输出确定。
//   - 重复的来源（按本包其他地方使用的 normalizePath 键，
//     在 Windows 上大小写折叠）会被去除，保留输入顺序中的
//     第一次出现。
//   - Source 为空/空白的指令会被丢弃：它们没有可排序的路径，
//     这与 RelevantInstructions 对缺失来源的处理一致。
//
// 这刻意与 Block 处于排序谱系的另一端，Block 记录的是
// "最具体的优先，较早者胜出"。OrderInstructions 不
// 改变 Block 或任何加载器；它只是为渲染器采用
// "后者胜出"约定的调用方提供的附加辅助函数。
func OrderInstructions(docs []Instruction) []Instruction {
	if len(docs) == 0 {
		return nil
	}

	type ranked struct {
		inst  Instruction
		depth int
		key   string
	}

	items := make([]ranked, 0, len(docs))
	seen := make(map[string]bool, len(docs))
	for _, d := range docs {
		if strings.TrimSpace(d.Source) == "" {
			continue
		}
		path := filepath.Clean(d.Source)
		key := normalizePath(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		items = append(items, ranked{inst: d, depth: pathDepth(path), key: key})
	}
	if len(items) == 0 {
		return nil
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].depth != items[j].depth {
			return items[i].depth < items[j].depth
		}
		return items[i].key < items[j].key
	})

	out := make([]Instruction, len(items))
	for i, it := range items {
		out[i] = it.inst
	}
	return out
}

// pathDepth 返回清理后路径的目录部分中目录分隔符的
// 数量。更深的目录（更靠近共享根之下的工作目录）
// 产生更大的数字，OrderInstructions 用它把更近的
// 指令放到后面。
func pathDepth(path string) int {
	return strings.Count(filepath.Dir(path), string(filepath.Separator))
}
