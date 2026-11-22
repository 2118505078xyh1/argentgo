package tui

import (
	"sort"
	"strings"
)

// 本文件把 keymap.go 中的纯别名原语接入一条 opt-in 的
// "原始键串 → 别名 → 动作名"旁路。默认关闭：没有注入别名配置时
// keyAliases 返回 nil，handleKeyLocked 根本不会进入旁路，
// 主 switch 与帧字节保持逐字节不变。

// keyAliasSpecEnv 是注入别名配置的变量名。格式是分号分隔的
// "动作名=别名1,别名2" 片段，例如 "scroll_up=k,K,上;submit=s"。
// 只有该变量非空时旁路才开启；空值等价于完全未配置。
const keyAliasSpecEnv = "ARGENT_KEY_ALIASES"

// DefaultKeyAliases 返回内置的默认别名表（动作名 → 别名列表）。
//
// 默认表只包含动作名自身的"规范键串"，不含任何可打印单字符别名，
// 因此它单独存在时不会接管任何按键（可打印键的键串是单个字符，
// 永远匹配不到 "scroll_up" 这样的长名字）。真正的单字符/CJK 别名
// 来自配置层，经 BuildKeyAliases 合并进来。这样的 fail-closed 默认
// 保证了"有表但无配置"与"完全无表"行为一致。
func DefaultKeyAliases() map[string][]string {
	return map[string][]string{
		"scroll_up":   {"scroll_up"},
		"scroll_down": {"scroll_down"},
		"page_up":     {"page_up"},
		"page_down":   {"page_down"},
		"submit":      {"submit"},
		"newline":     {"newline"},
		"undo":        {"undo"},
		"retry":       {"retry"},
	}
}

// ParseKeyAliasSpec 把 env/flag 注入的别名串解析为别名表。
//
// spec 以分号分隔片段，每个片段形如 "动作=别名1,别名2"。空白被裁剪，
// 空片段、无等号片段、空动作/空别名被忽略；重复别名经 addAlias
// 大小写不敏感地去重。该函数是纯函数，不读取全局状态。
func ParseKeyAliasSpec(spec string) map[string][]string {
	out := make(map[string][]string)
	for _, field := range strings.Split(spec, ";") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		i := strings.Index(field, "=")
		if i < 0 {
			continue
		}
		action := strings.TrimSpace(field[:i])
		if action == "" {
			continue
		}
		for _, alias := range strings.Split(field[i+1:], ",") {
			if alias = strings.TrimSpace(alias); alias != "" {
				addAlias(out, action, alias)
			}
		}
	}
	return out
}

// ParseKeyAliasBlock 从配置 map 中读取别名子块，仿 internal/config 的
// 纯函数风格：只读入参、fail-closed、不触碰磁盘或环境。
//
// 依次识别 "key_aliases" / "keybindings" / "key_bindings" 三个键，
// 其值须为 map[string]any；每个动作值可以是单个字符串、[]string，
// 或 []any（其中仅字符串元素生效）。动作名按字典序处理，结果确定。
func ParseKeyAliasBlock(raw map[string]any) map[string][]string {
	out := make(map[string][]string)
	if raw == nil {
		return out
	}
	for _, blockKey := range []string{"key_aliases", "keybindings", "key_bindings"} {
		block, ok := raw[blockKey].(map[string]any)
		if !ok {
			continue
		}
		names := make([]string, 0, len(block))
		for name := range block {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			switch v := block[name].(type) {
			case string:
				addAlias(out, name, v)
			case []string:
				for _, alias := range v {
					addAlias(out, name, alias)
				}
			case []any:
				for _, item := range v {
					if alias, ok := item.(string); ok {
						addAlias(out, name, alias)
					}
				}
			}
		}
	}
	return out
}

// BuildKeyAliases 把默认表、配置 map 子块与 env/flag 别名串合并为
// 最终别名表：默认表打底，配置块覆盖，env 串最后合并（同义别名会被
// addAlias 跳过）。合并全程经 mergeAliases，结果与 map 遍历顺序无关。
func BuildKeyAliases(raw map[string]any, spec string) map[string][]string {
	aliases := DefaultKeyAliases()
	mergeAliases(aliases, ParseKeyAliasBlock(raw))
	mergeAliases(aliases, ParseKeyAliasSpec(spec))
	return aliases
}

// ResolveKeyAction 在别名表中把原始键串解析为动作名，是 resolveBinding
// 的导出包装，供配置预览、帮助提示与测试直接调用。
// 大小写折叠、CJK 别名、歧义字典序等语义与 resolveBinding 完全一致。
func ResolveKeyAction(rawKey string, aliases map[string][]string) (action string, ok bool) {
	return resolveBinding(rawKey, aliases)
}

// keyAliases 返回当前模型生效的别名表；未注入别名配置时返回 nil。
//
// 门禁完全基于注入的 env 查找（run.go 用 os.Getenv 注入），TUI 自身
// 不直接读环境变量。spec 为空即视为未配置，旁路短路。
func (m *Model) keyAliases() map[string][]string {
	if m.env == nil {
		return nil
	}
	spec := strings.TrimSpace(m.env(keyAliasSpecEnv))
	if spec == "" {
		return nil
	}
	return BuildKeyAliases(nil, spec)
}

// tryAliasKeyLocked 在入口处尝试用别名表消费一次按键。
//
// 只对 KindRune（主 switch 中没有专属动作、原本只会插入文本的"未覆盖"
// 键）生效；已有专属 Kind 的键（方向键、Enter、Ctrl 组合等）始终由
// 主 switch 优先处理，别名仅作补充。未配置或命中失败时返回 false，
// 调用方照常走主 switch。
func (m *Model) tryAliasKeyLocked(key Key) (string, bool) {
	if key.Kind != KindRune || key.Rune == 0 {
		return "", false
	}
	aliases := m.keyAliases()
	if len(aliases) == 0 {
		return "", false
	}
	return ResolveKeyAction(string(key.Rune), aliases)
}

// applyAliasActionLocked 执行一个由别名解析出的动作名。
// 动作语义复用主 switch 的既有路径（滚动、提交、撤销等），返回
// 与主分派一致的 KeyAction；未知动作安静地当作空操作。
func (m *Model) applyAliasActionLocked(action string) KeyAction {
	switch action {
	case "scroll_up":
		m.scrollByLocked(3)
	case "scroll_down":
		m.scrollByLocked(-3)
	case "page_up":
		m.scrollByLocked(maxInt(m.bodyH, 10))
	case "page_down":
		m.scrollByLocked(-maxInt(m.bodyH, 10))
	case "submit":
		return m.submitLocked()
	case "newline":
		m.editor.Insert("\n")
		m.refreshCompletionLocked()
	case "clear_input":
		m.editor.Clear()
	case "abort", "cancel":
		if m.working {
			return ActionAbort
		}
	case "quit":
		m.quit = true
		return ActionQuit
	case "undo":
		return m.requestActionLocked(ActionUndo, "")
	case "retry":
		return m.requestActionLocked(ActionRetry, "")
	}
	return ActionNone
}
