package config

import "testing"

func TestDetectReplyLanguage(t *testing.T) {
	tests := []struct {
		name   string
		locale string
		env    map[string]string
		want   string
	}{
		{
			name:   "explicit locale wins over environment",
			locale: "zh-CN",
			env:    map[string]string{"LANG": "en_US.UTF-8"},
			want:   "zh",
		},
		{
			name:   "explicit English beats lower-priority Chinese LANG",
			locale: "en-US",
			env:    map[string]string{"LANG": "zh-CN"},
			want:   "en",
		},
		{
			name:   "explicit locale is trimmed and case-folded",
			locale: "  ZH_cn  ",
			env:    map[string]string{"LANG": "ja_JP"},
			want:   "zh",
		},
		{
			name:   "empty locale falls through to environment",
			locale: "",
			env:    map[string]string{"LANG": "ko-KR"},
			want:   "ko",
		},
		{
			name:   "auto locale falls through to environment",
			locale: "auto",
			env:    map[string]string{"LANG": "ja_JP.UTF-8"},
			want:   "ja",
		},
		{
			name:   "unknown locale falls through to environment",
			locale: "xx-YY",
			env:    map[string]string{"LANGUAGE": "fr_FR"},
			want:   "fr",
		},
		{
			name: "LANG Chinese",
			env:  map[string]string{"LANG": "zh_CN.UTF-8"},
			want: "zh",
		},
		{
			name: "LANGUAGE Japanese",
			env:  map[string]string{"LANGUAGE": "ja_JP"},
			want: "ja",
		},
		{
			name: "LC_ALL Korean",
			env:  map[string]string{"LC_ALL": "ko_KR.UTF-8"},
			want: "ko",
		},
		{
			name: "LC_MESSAGES Chinese modifier suffix",
			env:  map[string]string{"LC_MESSAGES": "zh_CN@euro"},
			want: "zh",
		},
		{
			name: "LANG beats LANGUAGE when both known",
			env: map[string]string{
				"LANG":     "en_US.UTF-8",
				"LANGUAGE": "zh_CN",
			},
			want: "en",
		},
		{
			name: "unknown LANG falls through to LANGUAGE",
			env: map[string]string{
				"LANG":     "xx_YY",
				"LANGUAGE": "ja_JP",
			},
			want: "ja",
		},
		{
			name: "full chain when earlier variables unknown",
			env: map[string]string{
				"LANG":        "xx_YY",
				"LANGUAGE":    "",
				"LC_ALL":      "zz_ZZ",
				"LC_MESSAGES": "ru_RU.UTF-8",
			},
			want: "ru",
		},
		{
			name: "POSIX C locale is English",
			env:  map[string]string{"LANG": "C"},
			want: "en",
		},
		{
			name: "POSIX C locale with encoding is English",
			env:  map[string]string{"LANG": "C.UTF-8"},
			want: "en",
		},
		{
			name: "POSIX locale is English",
			env:  map[string]string{"LC_ALL": "POSIX"},
			want: "en",
		},
		{
			name: "native CJK names",
			env:  map[string]string{"LANGUAGE": "繁體中文"},
			want: "zh",
		},
		{
			name: "Chinese script subtag",
			env:  map[string]string{"LANG": "zh-Hant-TW"},
			want: "zh",
		},
		{
			name: "all unknown falls back to English",
			env: map[string]string{
				"LANG":        "xx_YY",
				"LANGUAGE":    "zz_ZZ",
				"LC_ALL":      "qq_QQ",
				"LC_MESSAGES": "ww_WW",
			},
			want: "en",
		},
		{
			name: "nil env falls back to English",
			env:  nil,
			want: "en",
		},
		{
			name:   "nil env with unknown locale falls back to English",
			locale: "xx-YY",
			env:    nil,
			want:   "en",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectReplyLanguage(tt.locale, tt.env); got != tt.want {
				t.Errorf("DetectReplyLanguage(%q, %v) = %q, want %q", tt.locale, tt.env, got, tt.want)
			}
		})
	}
}

func TestLocaleDisplayName(t *testing.T) {
	tests := []struct {
		name string
		code string
		want string
	}{
		// 规范的短代码。
		{name: "English code", code: "en", want: "English"},
		{name: "Chinese code", code: "zh", want: "中文"},
		{name: "Japanese code", code: "ja", want: "日本語"},
		{name: "Korean code", code: "ko", want: "한국어"},
		{name: "Russian code", code: "ru", want: "Русский"},
		{name: "German code", code: "de", want: "Deutsch"},
		{name: "French code", code: "fr", want: "Français"},
		{name: "Portuguese code", code: "pt", want: "Português"},
		{name: "Spanish code", code: "es", want: "Español"},
		{name: "Catalan code", code: "ca", want: "Català"},

		// CJK 本地名称解析为相同的显示名。
		{name: "Chinese native name", code: "中文", want: "中文"},
		{name: "Simplified Chinese native name", code: "简体中文", want: "中文"},
		{name: "Traditional Chinese native name", code: "繁體中文", want: "中文"},
		{name: "Japanese native name", code: "日本語", want: "日本語"},
		{name: "Korean native name", code: "한국어", want: "한국어"},

		// 别名和 locale 拼写。
		{name: "three letter English alias", code: "eng", want: "English"},
		{name: "three letter Chinese alias", code: "chi", want: "中文"},
		{name: "zho alias", code: "zho", want: "中文"},
		{name: "full English name", code: "English", want: "English"},
		{name: "full Chinese name", code: "Chinese", want: "中文"},
		{name: "POSIX C locale", code: "C", want: "English"},
		{name: "POSIX locale", code: "POSIX", want: "English"},
		{name: "locale with region and encoding", code: "zh_CN.UTF-8", want: "中文"},
		{name: "BCP-47 script subtag", code: "zh-Hant-TW", want: "中文"},
		{name: "encoding modifier suffix", code: "ja_JP@euro", want: "日本語"},
		{name: "trimmed and case folded", code: "  DE_de  ", want: "Deutsch"},

		// 空值、中性值和未知值不产生显示名。
		{name: "empty string", code: "", want: ""},
		{name: "whitespace only", code: "   ", want: ""},
		{name: "auto sentinel", code: "auto", want: ""},
		{name: "AUTO sentinel case folded", code: "AUTO", want: ""},
		{name: "unknown code", code: "xx", want: ""},
		{name: "unsupported locale", code: "xx-YY", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LocaleDisplayName(tt.code); got != tt.want {
				t.Errorf("LocaleDisplayName(%q) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}

func TestLocaleDisplayNameRoundTripsDetectReplyLanguage(t *testing.T) {
	codes := []string{"zh", "en", "ja", "ko", "ru", "de", "fr", "pt", "es", "ca"}
	for _, code := range codes {
		if got := DetectReplyLanguage(code, nil); got != code {
			t.Errorf("DetectReplyLanguage(%q, nil) = %q, want %q", code, got, code)
		}
		if LocaleDisplayName(code) == "" {
			t.Errorf("LocaleDisplayName(%q) is empty; DetectReplyLanguage output must always have a display name", code)
		}
	}
}
