package tui

import (
	"errors"
	"fmt"
	"strings"
)

// Built-in theme names. The empty string always means ThemeDark so callers
// that have not been upgraded keep the original colours.
const (
	ThemeDark  = "dark"
	ThemeLight = "light"
	ThemeANSI  = "ansi"
)

// ErrUnknownTheme is returned by SetTheme for a name with no palette.
var ErrUnknownTheme = errors.New("tui: unknown theme")

// Palette assigns a colour to each semantic role used by the renderer. Themes
// are data: rendering code asks for a role and never names a raw colour, so a
// new theme is one more entry in themePalettes.
type Palette struct {
	User      Color
	Assistant Color
	Tool      Color
	Reasoning Color
	Error     Color
	Accent    Color
	Border    Color
	Muted     Color

	// Warning and Code cover the two roles the transcript uses beyond the
	// eight core ones: yellow notices/running tools and inline code.
	Warning Color
	Code    Color

	noColor bool // suppress even bold/dim; paired with Buffer.SetNoColor
}

var (
	colorBlack = Color{"30", "40"}
	colorWhite = Color{"37", "47"}
)

// themePalettes holds the three built-ins. All colours are plain 16-colour
// SGR codes; neither 256-colour (38;5) nor RGB (38;2) escapes are used, which
// keeps the ANSI theme safe on terminals without truecolor support.
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

// canonicalTheme folds aliases and the empty name onto a built-in.
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

// ThemeNames returns the built-in names in a stable order for help text.
func ThemeNames() []string { return []string{ThemeDark, ThemeLight, ThemeANSI} }

// paletteFor looks up a theme's palette. Unknown names return ErrUnknownTheme.
func paletteFor(name string) (Palette, error) {
	canonical := canonicalTheme(name)
	p, ok := themePalettes[canonical]
	if !ok {
		return Palette{}, fmt.Errorf("%w: %q", ErrUnknownTheme, name)
	}
	return p, nil
}

// monochrome drops every foreground/background while leaving bold and dim
// attributes intact. NO_COLOR and dumb terminals keep their text attributes.
func monochrome(p Palette) Palette {
	p.User, p.Assistant, p.Tool = ColorDefault, ColorDefault, ColorDefault
	p.Reasoning, p.Error, p.Accent = ColorDefault, ColorDefault, ColorDefault
	p.Border, p.Muted, p.Warning, p.Code = ColorDefault, ColorDefault, ColorDefault, ColorDefault
	return p
}

// detectDegradation reads only the injected environment. An empty lookup is a
// perfectly valid "no variables set" answer; os.Getenv is never consulted by
// the detection path itself.
func detectDegradation(env func(string) string) bool {
	if env == nil {
		return false
	}
	if value := strings.TrimSpace(env("NO_COLOR")); value != "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(env("TERM")), "dumb")
}

// Role styles. Everything the painter needs comes from these helpers so a
// theme change is a single palette swap, and NoColor can blank them all at once.
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
