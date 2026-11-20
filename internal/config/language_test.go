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
