package tui

import (
	"errors"
	"fmt"
	"strings"
)

// 内置主题名。空字符串始终表示 ThemeDark，使尚未
// 升级的调用方保持原始颜色。
const (
	ThemeDark  = "dark"
	ThemeLight = "light"
	ThemeANSI  = "ansi"
)

// ErrUnknownTheme 由 SetTheme 对没有调色板的名称返回。
var ErrUnknownTheme = errors.New("tui: unknown theme")

// Palette 为渲染器使用的每个语义角色分配颜色。主题
// 是数据：渲染代码请求一个角色，从不指定原始颜色，因此
// 新主题只是 themePalettes 中的另一个条目。
type Palette struct {
	User      Color
	Assistant Color
	Tool      Color
	Reasoning Color
	Error     Color
	Accent    Color
	Border    Color
	Muted     Color

	// Warning 和 Code 覆盖记录用到的、超出
	// 八个核心角色之外的两个角色：黄色通知/运行中工具和行内代码。
	Warning Color
	Code    Color

	// BadgeBGHex、BadgeInkHex、BadgeAltInkHex 是可选的徽章配色，
	// 仅供非内置主题填充。三个字段任一为空即视为未配置：内置主题
	// 全部留空，沿用上面的 SGR 角色走原路径，因此帧字节逐字不变。
	// 配置后由 LegibleInk 在原墨色与备选墨色间选出可读的徽章墨色。
	BadgeBGHex     string
	BadgeInkHex    string
	BadgeAltInkHex string

	noColor bool // suppress even bold/dim; paired with Buffer.SetNoColor
}

var (
	colorBlack = Color{"30", "40"}
	colorWhite = Color{"37", "47"}
)

// themePalettes 保存三个内置主题。所有颜色都是普通的 16 色
// SGR 码；既不用 256 色 (38;5) 也不用 RGB (38;2) 转义，这
// 使 ANSI 主题在不支持真彩的终端上安全。
var themePalettes = map[string]Palette{
	ThemeDark: {
		User: ColorCyan, Assistant: ColorDefault, Tool: ColorGreen,
		Reasoning: ColorMagenta, Error: ColorRed, Accent: ColorCyan,
		Border: ColorGray, Muted: ColorGray, Warning: ColorYellow, Code: ColorMagenta,
	},
	ThemeLight: {
		User: ColorBlue, Assistant: ColorDefault, Tool: ColorGreen,
		Reasoning: ColorMagenta, Error: ColorRed, Accent: ColorBlue,
		Border: colorBlack, Muted: colorBlack, Warning: ColorRed, Code: ColorBlue,
	},
	ThemeANSI: {
		User: ColorCyan, Assistant: ColorDefault, Tool: ColorGreen,
		Reasoning: ColorMagenta, Error: ColorRed, Accent: ColorCyan,
		Border: colorWhite, Muted: colorWhite, Warning: ColorYellow, Code: ColorMagenta,
	},
}

// canonicalTheme 把别名和空名称折叠到内置主题。
func canonicalTheme(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", ThemeDark:
		return ThemeDark
	case ThemeLight:
		return ThemeLight
	case ThemeANSI, "16", "16color", "16-color":
		return ThemeANSI
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

// ThemeNames 按稳定顺序返回内置名称，用于帮助文本。
func ThemeNames() []string { return []string{ThemeDark, ThemeLight, ThemeANSI} }

// paletteFor 查找主题的调色板。未知名称返回 ErrUnknownTheme。
func paletteFor(name string) (Palette, error) {
	canonical := canonicalTheme(name)
	p, ok := themePalettes[canonical]
	if !ok {
		return Palette{}, fmt.Errorf("%w: %q", ErrUnknownTheme, name)
	}
	return applyBadgeOverride(p), nil
}

// monochrome 去掉所有前景/背景，同时保留粗体和暗色
// 属性。NO_COLOR 和 dumb 终端保留其文本属性。
func monochrome(p Palette) Palette {
	p.User, p.Assistant, p.Tool = ColorDefault, ColorDefault, ColorDefault
	p.Reasoning, p.Error, p.Accent = ColorDefault, ColorDefault, ColorDefault
	p.Border, p.Muted, p.Warning, p.Code = ColorDefault, ColorDefault, ColorDefault, ColorDefault
	return p
}

// detectDegradation 只读取注入的环境。空的查找是
// 一个完全有效的"没有设置变量"答案；检测路径本身
// 从不查询 os.Getenv。
func detectDegradation(env func(string) string) bool {
	if env == nil {
		return false
	}
	if value := strings.TrimSpace(env("NO_COLOR")); value != "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(env("TERM")), "dumb")
}

// 角色样式。绘制器所需的一切都来自这些辅助函数，因此
// 主题变更只需替换一次调色板，且 NoColor 可一次性清空全部。
func (p Palette) user() Style {
	if p.noColor {
		return Style{}
	}
	return Style{Fg: p.User, Bold: true}
}

func (p Palette) assistant() Style {
	if p.noColor {
		return Style{}
	}
	return Style{Fg: p.Assistant}
}

func (p Palette) tool() Style {
	if p.noColor {
		return Style{}
	}
	return Style{Fg: p.Tool}
}

func (p Palette) reasoning() Style {
	if p.noColor {
		return Style{}
	}
	return Style{Fg: p.Reasoning}
}

func (p Palette) failure() Style {
	if p.noColor {
		return Style{}
	}
	return Style{Fg: p.Error, Bold: true}
}

func (p Palette) warning() Style {
	if p.noColor {
		return Style{}
	}
	return Style{Fg: p.Warning}
}

func (p Palette) code() Style {
	if p.noColor {
		return Style{}
	}
	return Style{Fg: p.Code}
}

func (p Palette) accent() Style {
	if p.noColor {
		return Style{}
	}
	return Style{Fg: p.Accent, Bold: true}
}

func (p Palette) border() Style {
	if p.noColor {
		return Style{}
	}
	return Style{Fg: p.Border}
}

func (p Palette) muted() Style {
	if p.noColor {
		return Style{}
	}
	return Style{Fg: p.Muted}
}

func (p Palette) dim() Style {
	if p.noColor {
		return Style{}
	}
	return Style{Fg: p.Muted, Dim: true}
}
