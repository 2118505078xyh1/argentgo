package tui

import (
	"strings"
	"sync/atomic"
)

type paletteBadgeOverride struct{ BG, Ink, AltInk string }

var badgeOverride atomic.Value // 存放 paletteBadgeOverride

// applyBadgeOverride 把已配置的 hex 角色写入调色板；无覆盖时原样返回。
func applyBadgeOverride(p Palette) Palette {
	if v, ok := badgeOverride.Load().(paletteBadgeOverride); ok {
		p.BadgeBGHex, p.BadgeInkHex, p.BadgeAltInkHex = v.BG, v.Ink, v.AltInk
	}
	return p
}

// 徽章对比度配色的独立门控：三个 hex 变量同时提供时，自定义主题的徽章墨色
// 由 LegibleInk 按对比度在 ink/altInk 间选择。未配置时内置/自定义主题都沿用
// 原 SGR 路径，帧字节逐字不变。
const (
	badgeBGEnv     = "ARGENT_TUI_BADGE_BG"
	badgeInkEnv    = "ARGENT_TUI_BADGE_INK"
	badgeAltInkEnv = "ARGENT_TUI_BADGE_ALT_INK"
)

// badgeContrastRoles 返回从 env 解析出的三个 hex 角色；任一为空则返回 nil（关闭）。
func badgeContrastRoles(env func(string) string) (bg, ink, alt string) {
	if env == nil {
		return "", "", ""
	}
	return strings.TrimSpace(env(badgeBGEnv)),
		strings.TrimSpace(env(badgeInkEnv)),
		strings.TrimSpace(env(badgeAltInkEnv))
}

// ConfigureBadgeContrast 在 opt-in 开启时把 hex 角色写入全局调色板覆盖，
// 供 Model 初始化时取用。它只设置进程级覆盖值，不触碰任何现有主题帧。
func ConfigureBadgeContrast(env func(string) string) {
	bg, ink, alt := badgeContrastRoles(env)
	if bg == "" || ink == "" || alt == "" {
		return
	}
	badgeOverride.Store(paletteBadgeOverride{BG: bg, Ink: ink, AltInk: alt})
}
