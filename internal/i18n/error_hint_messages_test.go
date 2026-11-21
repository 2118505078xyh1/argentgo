package i18n

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestErrorHintMessageEveryKey 验证表中每个键在中英文下都返回对应文案。
func TestErrorHintMessageEveryKey(t *testing.T) {
	for key, entry := range errorHintMessageTable {
		t.Run(key, func(t *testing.T) {
			if got := ErrorHintMessage(EnUS, key); got != entry.en {
				t.Errorf("ErrorHintMessage(%q, %q) = %q, want %q", EnUS, key, got, entry.en)
			}
			if got := ErrorHintMessage(ZhCN, key); got != entry.zh {
				t.Errorf("ErrorHintMessage(%q, %q) = %q, want %q", ZhCN, key, got, entry.zh)
			}
		})
	}
}

// TestErrorHintMessageKnownKeys 锁定四类 CLI 错误建议的文案，防止表被意外改动；
// 中文文案与 internal/cli/cli_error_hint.go 的现有建议逐字节一致。
func TestErrorHintMessageKnownKeys(t *testing.T) {
	cases := []struct {
		key string
		en  string
		zh  string
	}{
		{ErrorHintTLS,
			"TLS/certificate verification failed: check that the system time is correct, that the CA root certificates are complete, or that a proxy/corporate gateway is not replacing the certificate; retry after confirming",
			"TLS/证书校验失败：请检查系统时间是否正确、CA 根证书是否齐全，或代理/企业网关是否替换了证书；确认后重试"},
		{ErrorHintFile,
			"file not found: check the path spelling and the current working directory, confirm the file has been created, and retry with an absolute path if needed",
			"文件不存在：请检查路径拼写与当前工作目录，确认文件已创建，必要时改用绝对路径后重试"},
		{ErrorHintInteractive,
			"non-interactive environment: provide input through explicit flags such as --yes or command-line arguments, to avoid blocking when the terminal cannot be read",
			"当前为非交互环境：请改用 --yes 等显式标志或命令行参数提供输入，避免在无法读取终端时阻塞"},
		{ErrorHintCredentials,
			"missing or invalid credentials: run argent login to sign in again, or configure a correct API key and retry",
			"缺少或无效的凭证：请运行 argent login 重新登录，或配置正确的 API Key 后重试"},
	}
	for _, c := range cases {
		if got := ErrorHintMessage(EnUS, c.key); got != c.en {
			t.Errorf("ErrorHintMessage(%q, %q) = %q, want %q", EnUS, c.key, got, c.en)
		}
		if got := ErrorHintMessage(ZhCN, c.key); got != c.zh {
			t.Errorf("ErrorHintMessage(%q, %q) = %q, want %q", ZhCN, c.key, got, c.zh)
		}
	}
}

// TestErrorHintMessageFormatting 验证提供参数时按语言模板执行 fmt.Sprintf。
// 真实表的文案刻意不含格式动词（以保证无参调用的中文输出字节不变），因此这里
// 临时注入一条带动词的探针条目来覆盖格式化分支，测试结束即恢复，绝不污染真表。
func TestErrorHintMessageFormatting(t *testing.T) {
	const key = "errorhint.__format_probe__"
	original, existed := errorHintMessageTable[key]
	errorHintMessageTable[key] = errorHintMessageEntry{
		en: "run %s with %d retries",
		zh: "用 %d 次重试运行 %s",
	}
	defer func() {
		if existed {
			errorHintMessageTable[key] = original
		} else {
			delete(errorHintMessageTable, key)
		}
	}()

	cases := []struct {
		name string
		lang string
		args []any
		want string
	}{
		{"formatted en", EnUS, []any{"argent login", 3}, "run argent login with 3 retries"},
		{"formatted zh", ZhCN, []any{3, "argent login"}, "用 3 次重试运行 argent login"},
		{"no args en", EnUS, nil, "run %s with %d retries"},
		{"no args zh", ZhCN, nil, "用 %d 次重试运行 %s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ErrorHintMessage(c.lang, key, c.args...); got != c.want {
				t.Errorf("ErrorHintMessage(%q, %q, %v) = %q, want %q", c.lang, key, c.args, got, c.want)
			}
		})
	}
}

// TestErrorHintMessageLocaleAliases 验证语言环境别名解析到正确的语言文案。
func TestErrorHintMessageLocaleAliases(t *testing.T) {
	zh := errorHintMessageTable[ErrorHintCredentials].zh
	en := errorHintMessageTable[ErrorHintCredentials].en

	for _, lang := range []string{"zh", "zh-cn", "zh_cn", "chinese", "中文", "ZH-CN", "Zh-Cn"} {
		if got := ErrorHintMessage(lang, ErrorHintCredentials); got != zh {
			t.Errorf("lang %q: got %q, want Chinese %q", lang, got, zh)
		}
	}
	for _, lang := range []string{"en", "en-us", "en_us", "english", "EN-US", "English"} {
		if got := ErrorHintMessage(lang, ErrorHintCredentials); got != en {
			t.Errorf("lang %q: got %q, want English %q", lang, got, en)
		}
	}
}

// TestErrorHintMessageUnknownLocaleFallsBackToEn 验证未知或空语言环境回退到英文。
func TestErrorHintMessageUnknownLocaleFallsBackToEn(t *testing.T) {
	want := errorHintMessageTable[ErrorHintCredentials].en
	for _, lang := range []string{"fr-FR", "ja-JP", "not-a-locale", ""} {
		if got := ErrorHintMessage(lang, ErrorHintCredentials); got != want {
			t.Errorf("lang %q: got %q, want fallback %q", lang, got, want)
		}
	}
}

// TestErrorHintMessageMissingKey 验证未知键返回带键的稳定占位串且不 panic。
func TestErrorHintMessageMissingKey(t *testing.T) {
	for _, lang := range []string{EnUS, ZhCN, "fr-FR", "中文", ""} {
		const key = "errorhint.brand_new"
		first := ErrorHintMessage(lang, key, "ignored-arg")
		second := ErrorHintMessage(lang, key)
		if first == "" || first != second {
			t.Errorf("lang %q: unstable placeholder: %q vs %q", lang, first, second)
		}
		if !strings.Contains(first, key) {
			t.Errorf("lang %q: placeholder %q does not contain key %q", lang, first, key)
		}
	}
}

// TestErrorHintMessageKeysSortedAndStable 验证键列表稳定排序、覆盖全部键且返回独立切片。
func TestErrorHintMessageKeysSortedAndStable(t *testing.T) {
	got := ErrorHintMessageKeys()
	again := ErrorHintMessageKeys()
	if !reflect.DeepEqual(got, again) {
		t.Fatalf("ErrorHintMessageKeys not stable: %v vs %v", got, again)
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("ErrorHintMessageKeys not sorted: %v", got)
	}

	want := make([]string, 0, len(errorHintMessageTable))
	for key := range errorHintMessageTable {
		want = append(want, key)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ErrorHintMessageKeys = %v, want %v", got, want)
	}
	if len(got) != 4 {
		t.Errorf("ErrorHintMessageKeys count = %d, want 4: %v", len(got), got)
	}

	// 修改返回的切片不应影响表或后续调用。
	if len(got) > 0 {
		got[0] = "corrupted"
	}
	if !reflect.DeepEqual(ErrorHintMessageKeys(), want) {
		t.Errorf("mutating returned keys affected later calls: %v", ErrorHintMessageKeys())
	}
}

// TestErrorHintMessageCJK 验证含 CJK 的文案与键保持完整 UTF-8 字节序列。
func TestErrorHintMessageCJK(t *testing.T) {
	for _, key := range ErrorHintMessageKeys() {
		zh := ErrorHintMessage(ZhCN, key)
		if !utf8.ValidString(zh) || !containsCJK(zh) {
			t.Errorf("key %q: Chinese message not valid CJK: %q", key, zh)
		}
	}

	// 未知 CJK 键应原样出现在占位串中，不丢失或截断任何字节。
	const cjkKey = "errorhint.配额不足：链接已失效 🪙"
	for _, lang := range []string{EnUS, ZhCN, "中文"} {
		got := ErrorHintMessage(lang, cjkKey)
		if !utf8.ValidString(got) || !strings.Contains(got, cjkKey) {
			t.Errorf("lang %q: CJK key corrupted in placeholder: %q", lang, got)
		}
	}
}

// TestErrorHintMessageDoesNotMutateArgs 验证调用不修改传入的参数切片或其指向的数据。
func TestErrorHintMessageDoesNotMutateArgs(t *testing.T) {
	args := []any{"argent login", 7}
	original := append([]any(nil), args...)
	_ = ErrorHintMessage(ZhCN, ErrorHintCredentials, args...)
	if !reflect.DeepEqual(args, original) {
		t.Errorf("args slice mutated: got %v, want %v", args, original)
	}

	data := []int{1, 2, 3}
	snapshot := []int{1, 2, 3}
	_ = ErrorHintMessage(EnUS, ErrorHintFile, &data)
	if !reflect.DeepEqual(data, snapshot) {
		t.Errorf("pointed-to data mutated: got %v, want %v", data, snapshot)
	}
}

// TestErrorHintMessagePure 验证 ErrorHintMessage 不向 Missing 记录任何内容。
func TestErrorHintMessagePure(t *testing.T) {
	ResetMissing()
	_ = ErrorHintMessage(ZhCN, ErrorHintTLS)
	_ = ErrorHintMessage(EnUS, "errorhint.brand_new")
	if got := Missing(); len(got) != 0 {
		t.Errorf("ErrorHintMessage recorded missing keys: %v", got)
	}
}
