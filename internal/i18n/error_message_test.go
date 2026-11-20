package i18n

import (
	"testing"
	"unicode/utf8"
)

// TestErrorMessageEveryCode 验证映射表中每个错误码在中英文下都返回对应文案。
func TestErrorMessageEveryCode(t *testing.T) {
	for code, entry := range errorMessageTable {
		t.Run(code, func(t *testing.T) {
			if got := errorMessage(EnUS, code); got != entry.en {
				t.Errorf("errorMessage(%q, %q) = %q, want %q", EnUS, code, got, entry.en)
			}
			if got := errorMessage(ZhCN, code); got != entry.zh {
				t.Errorf("errorMessage(%q, %q) = %q, want %q", ZhCN, code, got, entry.zh)
			}
		})
	}
}

// TestErrorMessageKnownCodes 锁定任务点名的常见错误码文案，防止表被意外改动。
func TestErrorMessageKnownCodes(t *testing.T) {
	cases := []struct {
		code string
		en   string
		zh   string
	}{
		{"auth_required", "authentication required", "需要登录认证"},
		{"credits_exhausted", "credits exhausted", "额度已用尽"},
		{"rate_limit", "rate limit exceeded", "请求过于频繁，请稍后重试"},
		{"context_overflow", "context length exceeded", "上下文长度超出限制"},
	}
	for _, c := range cases {
		if got := errorMessage(EnUS, c.code); got != c.en {
			t.Errorf("errorMessage(%q, %q) = %q, want %q", EnUS, c.code, got, c.en)
		}
		if got := errorMessage(ZhCN, c.code); got != c.zh {
			t.Errorf("errorMessage(%q, %q) = %q, want %q", ZhCN, c.code, got, c.zh)
		}
	}
}

// TestErrorMessageUnknownCode 验证未知错误码在任意语言环境下都原样返回。
func TestErrorMessageUnknownCode(t *testing.T) {
	for _, locale := range []string{EnUS, ZhCN, "fr-FR", "", "not-a-locale"} {
		const code = "brand_new_error_code"
		if got := errorMessage(locale, code); got != code {
			t.Errorf("errorMessage(%q, %q) = %q, want original code", locale, code, got)
		}
	}
}

// TestErrorMessageLocaleAliases 验证 locale 别名解析到正确的语言文案。
func TestErrorMessageLocaleAliases(t *testing.T) {
	const code = "auth_required"
	zh := errorMessageTable[code].zh
	en := errorMessageTable[code].en

	zhLocales := []string{"zh", "zh-cn", "zh_cn", "chinese", "中文"}
	for _, locale := range zhLocales {
		if got := errorMessage(locale, code); got != zh {
			t.Errorf("locale %q: got %q, want Chinese %q", locale, got, zh)
		}
	}
	enLocales := []string{"en", "en-us", "en_us", "english"}
	for _, locale := range enLocales {
		if got := errorMessage(locale, code); got != en {
			t.Errorf("locale %q: got %q, want English %q", locale, got, en)
		}
	}
}

// TestErrorMessageLocaleCaseInsensitive 验证 locale 大小写变体得到一致的文案。
func TestErrorMessageLocaleCaseInsensitive(t *testing.T) {
	const code = "rate_limit"
	zh := errorMessageTable[code].zh
	en := errorMessageTable[code].en

	for _, locale := range []string{"ZH-CN", "Zh-Cn", "ZH_CN", "ZH", "中文"} {
		if got := errorMessage(locale, code); got != zh {
			t.Errorf("locale %q: got %q, want Chinese %q", locale, got, zh)
		}
	}
	for _, locale := range []string{"EN-US", "En-uS", "EN_US", "EN", "English"} {
		if got := errorMessage(locale, code); got != en {
			t.Errorf("locale %q: got %q, want English %q", locale, got, en)
		}
	}
}

// TestErrorMessageUnsupportedLocaleFallsBack 验证未知 locale 回退到默认英文文案。
func TestErrorMessageUnsupportedLocaleFallsBack(t *testing.T) {
	const code = "context_overflow"
	want := errorMessageTable[code].en
	for _, locale := range []string{"fr-FR", "ja-JP", "not-a-locale", ""} {
		if got := errorMessage(locale, code); got != want {
			t.Errorf("locale %q: got %q, want default %q", locale, got, want)
		}
	}
}

// TestErrorMessageCJKSafety 验证含 CJK 的文案与 code 都保持完整的 UTF-8 字节序列。
func TestErrorMessageCJKSafety(t *testing.T) {
	// 已知码的中文文案本身含 CJK，必须与表内容逐字节相等。
	const code = "rate_limit"
	zh := errorMessage(ZhCN, code)
	if !utf8.ValidString(zh) || zh != errorMessageTable[code].zh {
		t.Errorf("Chinese message corrupted: %q", zh)
	}

	// 未知 CJK code 应原样返回，不丢失或截断任何字节。
	const cjkCode = "配额不足：链接已失效 🪙"
	for _, locale := range []string{EnUS, ZhCN, "中文"} {
		got := errorMessage(locale, cjkCode)
		if !utf8.ValidString(got) || got != cjkCode {
			t.Errorf("locale %q: CJK code corrupted: got %q, want %q", locale, got, cjkCode)
		}
	}

	// CJK locale 别名同样解析为完整的中文文案。
	got := errorMessage("中文", code)
	if !utf8.ValidString(got) || got != errorMessageTable[code].zh {
		t.Errorf("CJK locale produced corrupted message: %q", got)
	}
}

// TestErrorMessagePure 验证 errorMessage 不向 Missing 记录任何内容。
func TestErrorMessagePure(t *testing.T) {
	ResetMissing()
	_ = errorMessage(ZhCN, "auth_required")
	_ = errorMessage(EnUS, "brand_new_error_code")
	if got := Missing(); len(got) != 0 {
		t.Errorf("errorMessage recorded missing keys: %v", got)
	}
}
