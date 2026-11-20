package tui

// UI 偏好快照中的键。它们是持久化格式的稳定字段：固定名称、固定集合，
// 不随运行的 Go 版本或 map 随机迭代顺序改变，因此写出的快照可复现且可回读。
const (
	uiPrefKeyTheme       = "theme"
	uiPrefKeyCompactMode = "compact_mode"
	uiPrefKeyNoColor     = "no_color"
)

// UIPreferences 是 UI 偏好的可快照表示：主题名、紧凑模式与无颜色开关。
// 它只包含偏好本身，不含会话、凭据或任何瞬态 UI 状态，便于上层把它当作
// 不可变快照持久化（Crush 风格）。Theme 原样保存字符串，不在这里做
// 规范化或截断，因此中文等 CJK 主题名可完整往返。
type UIPreferences struct {
	Theme       string
	CompactMode bool
	NoColor     bool
}

// preferencesFromModel 从模型级的 UI 偏好原始值构造快照。
// theme 是当前主题名（可含 CJK），compact 与 noColor 是对应的布尔开关。
// 函数只做字段搬运：不读取全局状态、不规范化主题名、不按字节切分字符串，
// 因此 CJK 主题名安全，且对相同的入参总得到相同的结果。
func preferencesFromModel(theme string, compact, noColor bool) UIPreferences {
	return UIPreferences{
		Theme:       theme,
		CompactMode: compact,
		NoColor:     noColor,
	}
}

// preferencesToMap 把偏好快照编码为字符串键的映射，供持久化层写出。
// 映射始终包含全部三个稳定字段（即使为零值），使写出的结构不因偏好取值
// 而增减键。主题名按原样放入，不做字节切片或截断，CJK 名称完整保留。
func preferencesToMap(p UIPreferences) map[string]any {
	return map[string]any{
		uiPrefKeyTheme:       p.Theme,
		uiPrefKeyCompactMode: p.CompactMode,
		uiPrefKeyNoColor:     p.NoColor,
	}
}

// preferencesFromMap 是 preferencesToMap 的逆操作，用于从持久化映射还原
// 偏好快照，从而让结构与映射之间形成完整的往返。缺失键、类型不符或 nil
// 映射都回退到对应的零值，绝不 panic。主题名按字符串原样读取，CJK 安全。
func preferencesFromMap(m map[string]any) UIPreferences {
	var p UIPreferences
	if m == nil {
		return p
	}
	if theme, ok := m[uiPrefKeyTheme].(string); ok {
		p.Theme = theme
	}
	if compact, ok := m[uiPrefKeyCompactMode].(bool); ok {
		p.CompactMode = compact
	}
	if noColor, ok := m[uiPrefKeyNoColor].(bool); ok {
		p.NoColor = noColor
	}
	return p
}
