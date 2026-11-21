package config

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// aliasSpellings 把规范键名映射到它所有可接受的别名拼写。
// 别名的事实来源是 schema 与 diagnostics 中已有的表，集中在这里
// 供未知键检测复用，避免在多处重复维护别名清单。
var (
	aliasSpellingsOnce sync.Once
	aliasSpellings     map[string]map[string]bool
)

// knownAliasSpellings 返回规范键名到别名拼写集合的映射。
// 结果只依赖包内固定表，计算一次后缓存；不读取环境或磁盘。
func knownAliasSpellings() map[string]map[string]bool {
	aliasSpellingsOnce.Do(func() {
		aliasSpellings = make(map[string]map[string]bool)
		add := func(canonical, alias string) {
			if canonical == "" || alias == "" || canonical == alias {
				return
			}
			if aliasSpellings[canonical] == nil {
				aliasSpellings[canonical] = make(map[string]bool)
			}
			aliasSpellings[canonical][alias] = true
		}

		// schema 是别名的主要来源，包含各键在 x-aliases 中登记的拼写。
		var doc configSchemaDocument
		if err := json.Unmarshal(configSchemaJSON(), &doc); err == nil {
			for name, prop := range doc.Properties {
				for _, alias := range prop.Aliases {
					add(name, alias)
				}
			}
		}
		// diagnostics 的别名家族是另一处事实来源，合并进来保持一致。
		for name, spellings := range configAliases {
			for _, alias := range spellings {
				add(name, alias)
			}
		}
		// 下列嵌套键没有进入 schema，但 decode 确实接受它们的别名拼写。
		for name, spellings := range map[string][]string{
			"contextWindowInfo": {"context_window_info"},
			"ropeScaling":       {"rope_scaling"},
			"originalMax":       {"original_max"},
		} {
			for _, alias := range spellings {
				add(name, alias)
			}
		}
	})
	return aliasSpellings
}

// knownPath 保存一条可接受键路径的原始文本与分段结果。
// 原始文本用于精确匹配（即便键名本身含点也正确），分段用于别名匹配。
type knownPath struct {
	raw  string
	segs []string
}

// findUnknownKeys 返回 tree 中未被 known 认可的键路径，路径以 "." 分隔。
//
// 判定规则：
//   - known 是可接受键的点分路径清单。路径的每一段既可以是规范键名，
//     也可以是该键在 schema/configAliases 中登记的别名拼写，
//     因此合法的别名拼写不会被当作未知键（"排除已知别名"）。
//   - 只递归进入被认可的容器键；遇到未知键即记录其完整路径，不再深入
//     其子树——这与 codex strict 模式"只报告最外层未知字段"的语义一致。
//   - 结果去重后按字典序稳定排序；键名含中文等 CJK 字符时按原样比较，
//     不做任何 ASCII 或正则假设。
//
// 该函数是纯函数：不修改 tree 或 known，不读取环境或磁盘，不产生副作用；
// 相同输入始终得到相同输出。
func findUnknownKeys(tree map[string]any, known []string) []string {
	// 预先拆分 known，避免对每个树节点重复切分。
	candidates := make([]knownPath, 0, len(known))
	for _, raw := range known {
		if raw == "" {
			continue
		}
		candidates = append(candidates, knownPath{raw: raw, segs: strings.Split(raw, ".")})
	}

	aliases := knownAliasSpellings()
	matches := func(path string, segs []string) bool {
		for _, candidate := range candidates {
			// 精确匹配优先，键名本身含 "." 时也不会被误拆。
			if path == candidate.raw {
				return true
			}
			if len(segs) != len(candidate.segs) {
				continue
			}
			ok := true
			for i, segment := range segs {
				if segment == candidate.segs[i] {
					continue
				}
				if spellings, found := aliases[candidate.segs[i]]; found && spellings[segment] {
					continue
				}
				ok = false
				break
			}
			if ok {
				return true
			}
		}
		return false
	}

	found := make(map[string]bool)
	var walk func(node map[string]any, prefix string)
	walk = func(node map[string]any, prefix string) {
		for key, value := range node {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if !matches(path, strings.Split(path, ".")) {
				found[path] = true
				continue // 未知子树整体视为未知，不再深入。
			}
			if child, ok := value.(map[string]any); ok {
				walk(child, path)
			}
		}
	}
	walk(tree, "")

	out := make([]string, 0, len(found))
	for path := range found {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
