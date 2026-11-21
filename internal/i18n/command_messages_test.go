package i18n

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCommandMessageEveryKey 验证表中每个键在中英文下都返回对应文案。
func TestCommandMessageEveryKey(t *testing.T) {
	for key, entry := range commandMessageTable {
		t.Run(key, func(t *testing.T) {
			if got := CommandMessage(EnUS, key); got != entry.en {
				t.Errorf("CommandMessage(%q, %q) = %q, want %q", EnUS, key, got, entry.en)
			}
			if got := CommandMessage(ZhCN, key); got != entry.zh {
				t.Errorf("CommandMessage(%q, %q) = %q, want %q", ZhCN, key, got, entry.zh)
			}
		})
	}
}

// TestCommandMessageKnownKeys 锁定任务点名的命令消息文案，防止表被意外改动。
func TestCommandMessageKnownKeys(t *testing.T) {
	cases := []struct {
		key string
		en  string
		zh  string
	}{
		{CommandShellTimeout, "command timed out after %s", "命令执行超时：%s"},
		{CommandShellExitCode, "exit code %d", "退出码 %d"},
		{CommandShellKilledSignal, "killed by signal %s", "被信号 %s 终止"},
		{CommandShellTruncated, "output truncated to %d bytes", "输出已截断至 %d 字节"},
		{CommandShellNoOutput, "command produced no output", "命令未产生输出"},
		{CommandShellSpillPath, "full output written to %s", "完整输出已写入 %s"},
	}
	for _, c := range cases {
		if got := CommandMessage(EnUS, c.key); got != c.en {
			t.Errorf("CommandMessage(%q, %q) = %q, want %q", EnUS, c.key, got, c.en)
		}
		if got := CommandMessage(ZhCN, c.key); got != c.zh {
			t.Errorf("CommandMessage(%q, %q) = %q, want %q", ZhCN, c.key, got, c.zh)
		}
	}
}

// TestCommandMessageFormatting 验证在提供参数时按语言模板执行 fmt.Sprintf。
func TestCommandMessageFormatting(t *testing.T) {
	cases := []struct {
		name string
		lang string
		key  string
		args []any
		want string
	}{
		{"timeout en", EnUS, CommandShellTimeout, []any{"30s"}, "command timed out after 30s"},
		{"timeout zh", ZhCN, CommandShellTimeout, []any{"30s"}, "命令执行超时：30s"},
		{"exit code en", EnUS, CommandShellExitCode, []any{127}, "exit code 127"},
		{"exit code zh", ZhCN, CommandShellExitCode, []any{127}, "退出码 127"},
		{"signal en", EnUS, CommandShellKilledSignal, []any{"SIGKILL"}, "killed by signal SIGKILL"},
		{"signal zh", ZhCN, CommandShellKilledSignal, []any{"SIGTERM"}, "被信号 SIGTERM 终止"},
		{"truncated en", EnUS, CommandShellTruncated, []any{1048576}, "output truncated to 1048576 bytes"},
		{"truncated zh", ZhCN, CommandShellTruncated, []any{1024}, "输出已截断至 1024 字节"},
		{"spill path en", EnUS, CommandShellSpillPath, []any{"/tmp/out.log"}, "full output written to /tmp/out.log"},
		{"spill path zh", ZhCN, CommandShellSpillPath, []any{"/tmp/out.log"}, "完整输出已写入 /tmp/out.log"},
		{"started en", EnUS, CommandShellStarted, []any{"go test ./..."}, "running command: go test ./..."},
		{"started zh", ZhCN, CommandShellStarted, []any{"go test ./..."}, "正在执行命令：go test ./..."},
		{"no args", ZhCN, CommandShellNoOutput, nil, "命令未产生输出"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CommandMessage(c.lang, c.key, c.args...); got != c.want {
				t.Errorf("CommandMessage(%q, %q, %v) = %q, want %q", c.lang, c.key, c.args, got, c.want)
			}
		})
	}
}

// TestCommandMessageLocaleAliases 验证语言环境别名解析到正确的语言文案。
func TestCommandMessageLocaleAliases(t *testing.T) {
	zh := commandMessageTable[CommandShellTimeout].zh
	en := commandMessageTable[CommandShellTimeout].en

	for _, lang := range []string{"zh", "zh-cn", "zh_cn", "chinese", "中文", "ZH-CN", "Zh-Cn"} {
		if got := CommandMessage(lang, CommandShellTimeout); got != zh {
			t.Errorf("lang %q: got %q, want Chinese %q", lang, got, zh)
		}
	}
	for _, lang := range []string{"en", "en-us", "en_us", "english", "EN-US", "English"} {
		if got := CommandMessage(lang, CommandShellTimeout); got != en {
			t.Errorf("lang %q: got %q, want English %q", lang, got, en)
		}
	}
}

// TestCommandMessageUnknownLocaleFallsBackToEn 验证未知或空语言环境回退到英文。
func TestCommandMessageUnknownLocaleFallsBackToEn(t *testing.T) {
	want := "exit code 1"
	for _, lang := range []string{"fr-FR", "ja-JP", "not-a-locale", ""} {
		if got := CommandMessage(lang, CommandShellExitCode, 1); got != want {
			t.Errorf("lang %q: got %q, want fallback %q", lang, got, want)
		}
	}
}

// TestCommandMessageMissingKey 验证未知键返回带键的稳定占位串且不 panic。
func TestCommandMessageMissingKey(t *testing.T) {
	for _, lang := range []string{EnUS, ZhCN, "fr-FR", "中文", ""} {
		const key = "shell.brand_new"
		first := CommandMessage(lang, key, "ignored-arg")
		second := CommandMessage(lang, key)
		if first == "" || first != second {
			t.Errorf("lang %q: unstable placeholder: %q vs %q", lang, first, second)
		}
		if !strings.Contains(first, key) {
			t.Errorf("lang %q: placeholder %q does not contain key %q", lang, first, key)
		}
	}
}

// TestCommandMessageKeysSortedAndStable 验证键列表稳定排序、覆盖全部键且返回独立切片。
func TestCommandMessageKeysSortedAndStable(t *testing.T) {
	got := CommandMessageKeys()
	again := CommandMessageKeys()
	if !reflect.DeepEqual(got, again) {
		t.Fatalf("CommandMessageKeys not stable: %v vs %v", got, again)
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("CommandMessageKeys not sorted: %v", got)
	}

	want := make([]string, 0, len(commandMessageTable))
	for key := range commandMessageTable {
		want = append(want, key)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CommandMessageKeys = %v, want %v", got, want)
	}

	// 修改返回的切片不应影响表或后续调用。
	if len(got) > 0 {
		got[0] = "corrupted"
	}
	if !reflect.DeepEqual(CommandMessageKeys(), want) {
		t.Errorf("mutating returned keys affected later calls: %v", CommandMessageKeys())
	}
}

// TestCommandMessageCJK 验证含 CJK 的文案与键保持完整 UTF-8 字节序列。
func TestCommandMessageCJK(t *testing.T) {
	for _, key := range CommandMessageKeys() {
		zh := CommandMessage(ZhCN, key)
		if !utf8.ValidString(zh) || !containsCJK(zh) {
			t.Errorf("key %q: Chinese message not valid CJK: %q", key, zh)
		}
	}

	// 未知 CJK 键应原样出现在占位串中，不丢失或截断任何字节。
	const cjkKey = "shell.配额不足：链接已失效 🪙"
	for _, lang := range []string{EnUS, ZhCN, "中文"} {
		got := CommandMessage(lang, cjkKey)
		if !utf8.ValidString(got) || !strings.Contains(got, cjkKey) {
			t.Errorf("lang %q: CJK key corrupted in placeholder: %q", lang, got)
		}
	}
}

// TestCommandMessageDoesNotMutateArgs 验证调用不修改传入的参数切片或其指向的数据。
func TestCommandMessageDoesNotMutateArgs(t *testing.T) {
	args := []any{"SIGTERM", 7}
	original := append([]any(nil), args...)
	_ = CommandMessage(ZhCN, CommandShellKilledSignal, args...)
	if !reflect.DeepEqual(args, original) {
		t.Errorf("args slice mutated: got %v, want %v", args, original)
	}

	data := []int{1, 2, 3}
	snapshot := []int{1, 2, 3}
	_ = CommandMessage(EnUS, CommandShellExitCode, &data)
	if !reflect.DeepEqual(data, snapshot) {
		t.Errorf("pointed-to data mutated: got %v, want %v", data, snapshot)
	}
}

// TestCommandMessagePure 验证 CommandMessage 不向 Missing 记录任何内容。
func TestCommandMessagePure(t *testing.T) {
	ResetMissing()
	_ = CommandMessage(ZhCN, CommandShellTimeout, "1s")
	_ = CommandMessage(EnUS, "shell.brand_new")
	if got := Missing(); len(got) != 0 {
		t.Errorf("CommandMessage recorded missing keys: %v", got)
	}
}

// containsCJK 报告 s 中是否含 CJK 统一表意文字字符。
func containsCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}
