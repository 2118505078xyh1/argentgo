package shell

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/jobs"
	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
)

// 输出对账是进程级 opt-in。所有用例都不并行，结束时显式关回，
// 避免开启状态泄漏到其它测试，破坏"默认关、逐字节不变"的契约。

// requireBashShell 在需要真实 POSIX shell 的用例前跳过不可用环境
// （Windows 上的 WindowsApps bash.exe 存根不是可用的 shell）。
func requireBashShell(t *testing.T) {
	t.Helper()
	path, _ := shellInvocation("")
	if path == "cmd" {
		t.Skip("需要 POSIX shell")
	}
	resolved, err := exec.LookPath(path)
	if err != nil || strings.Contains(resolved, "WindowsApps") {
		t.Skip("系统 shell 不可用或是 WindowsApps 存根")
	}
}

// TestOutputIntegrityNoteSwitchOff 验证开关关闭时无论输入输出如何都返回空串，
// 且 bashNoOutput 逐字节返回现状的 "(no output)"。
func TestOutputIntegrityNoteSwitchOff(t *testing.T) {
	SetOutputAccounting(false)
	defer SetOutputAccounting(false)

	cases := []struct {
		raw, delivered string
		rawBytes       int
	}{
		{"", "", 0},
		{"\x1b[2K\r", "", 5},
		{"中文😀", "", 10},
		{"abc", "abc", 3},
	}
	for _, tc := range cases {
		if got := OutputIntegrityNote(tc.raw, tc.delivered, tc.rawBytes); got != "" {
			t.Fatalf("开关关闭时 note = %q，want 空串", got)
		}
	}
	for _, raw := range []string{"", "\x1b[2K\r", "中文😀", strings.Repeat("x", 100)} {
		if got := bashNoOutput(raw, ""); got != "(no output)" {
			t.Fatalf("开关关闭时 bashNoOutput(%q) = %q，want %q", raw, got, "(no output)")
		}
	}
}

// TestNoOutputBaselineThreePathsSwitchOff 覆盖三处 (no output) 分支在
// 开关关闭时的逐字节基线：必须是现状的 "(no output)"，不带任何标注。
func TestNoOutputBaselineThreePathsSwitchOff(t *testing.T) {
	SetOutputAccounting(false)
	defer SetOutputAccounting(false)

	t.Run("system shell", func(t *testing.T) {
		requireBashShell(t)
		t.Setenv("ARGENT_BUILTIN_SHELL", "0")
		got, err := runBashTool(t, "true")
		if err != nil {
			t.Fatal(err)
		}
		if got != "(no output)" {
			t.Fatalf("系统 shell 静默输出 = %q，want %q", got, "(no output)")
		}
	})

	t.Run("builtin shell", func(t *testing.T) {
		t.Setenv("ARGENT_BUILTIN_SHELL", "1")
		got, err := runBashTool(t, "true")
		if err != nil {
			t.Fatal(err)
		}
		if got != "(no output)" {
			t.Fatalf("内建 shell 静默输出 = %q，want %q", got, "(no output)")
		}
	})

	t.Run("promotable", func(t *testing.T) {
		requireBashShell(t)
		reg, err := jobs.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		ctx := pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{Jobs: reg})
		path, argv := shellInvocation("true")
		got, err := runPromotableBash(ctx, t.TempDir(), reg, "true", path, argv, 30*time.Second, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if got != "(no output)" {
			t.Fatalf("可提升路径静默输出 = %q，want %q", got, "(no output)")
		}
	})
}

// TestOutputIntegrityNoteNeutralZeroBytes 验证合法零输出得到中性标注，
// 且通过 bashNoOutput 正确附在 "(no output)" 之后。
func TestOutputIntegrityNoteNeutralZeroBytes(t *testing.T) {
	SetOutputAccounting(true)
	defer SetOutputAccounting(false)

	const want = "[output: 0 bytes]"
	if got := OutputIntegrityNote("", "", 0); got != want {
		t.Fatalf("零字节 note = %q，want %q", got, want)
	}
	if got := bashNoOutput("", ""); got != "(no output)\n"+want {
		t.Fatalf("零字节 bashNoOutput = %q，want %q", got, "(no output)\n"+want)
	}
}

// TestOutputIntegrityNoteLossyStable 验证 raw 有字节但清洗归零时得到 lossy 标注：
// 入参不被修改、重复调用结果稳定、标注是合法 UTF-8。
func TestOutputIntegrityNoteLossyStable(t *testing.T) {
	SetOutputAccounting(true)
	defer SetOutputAccounting(false)

	raw := "\x1b[2K\r" // ESC [ 2 K CR：ANSI 擦行 + 回车，清洗后为空
	rawCopy := raw
	delivered := clipBashOutput(context.Background(), raw)
	if delivered != "" {
		t.Fatalf("测试前提失败：清洗后应为空白，got %q", delivered)
	}

	const want = "[output: 5 bytes raw → 0 lines delivered, sanitized empty]"
	note := OutputIntegrityNote(raw, delivered, len(raw))
	if note != want {
		t.Fatalf("lossy note = %q，want %q", note, want)
	}
	if note2 := OutputIntegrityNote(raw, delivered, len(raw)); note2 != note {
		t.Fatalf("重复调用不稳定：%q != %q", note2, note)
	}
	if raw != rawCopy || delivered != "" {
		t.Fatalf("入参被修改：raw=%q delivered=%q", raw, delivered)
	}
	if !utf8.ValidString(note) {
		t.Fatalf("标注不是合法 UTF-8：%q", note)
	}
}

// TestOutputIntegrityNoteLossyEndToEnd 通过系统 shell 产生一条
// "raw 有 5 字节但清洗归零" 的真实输出，验证 lossy 标注被附上且 error==nil。
func TestOutputIntegrityNoteLossyEndToEnd(t *testing.T) {
	SetOutputAccounting(true)
	defer SetOutputAccounting(false)
	requireBashShell(t)
	t.Setenv("ARGENT_BUILTIN_SHELL", "0")

	got, err := runBashTool(t, `printf '\033[2K\r'`)
	if err != nil {
		t.Fatal(err)
	}
	const want = "(no output)\n[output: 5 bytes raw → 0 lines delivered, sanitized empty]"
	if got != want {
		t.Fatalf("lossy 端到端 = %q，want %q", got, want)
	}
}

// TestOutputIntegrityNoteCJKAndEmoji 验证 CJK/emoji 按字节计数，
// 标注仍是合法 UTF-8。
func TestOutputIntegrityNoteCJKAndEmoji(t *testing.T) {
	SetOutputAccounting(true)
	defer SetOutputAccounting(false)

	raw := "中文😀" // 中文各 3 字节 + emoji 4 字节 = 10
	if len(raw) != 10 {
		t.Fatalf("测试前提失败：raw 字节数 = %d，want 10", len(raw))
	}
	note := OutputIntegrityNote(raw, "", len(raw))
	const want = "[output: 10 bytes raw → 0 lines delivered, sanitized empty]"
	if note != want {
		t.Fatalf("CJK/emoji note = %q，want %q", note, want)
	}
	if !utf8.ValidString(note) {
		t.Fatalf("标注不是合法 UTF-8：%q", note)
	}
}

// TestOutputIntegrityNoteNonEmptyDelivered 验证 delivered 非空时不重复标注：
// 超长裁剪已有独立 marker，不能再加一条。
func TestOutputIntegrityNoteNonEmptyDelivered(t *testing.T) {
	SetOutputAccounting(true)
	defer SetOutputAccounting(false)

	if got := OutputIntegrityNote("abc", "abc", 3); got != "" {
		t.Fatalf("delivered 非空时 note = %q，want 空串", got)
	}
	// delivered 仅空白仍属于"清洗归零"：调用方据此判定为 "(no output)"。
	if got := OutputIntegrityNote("abc", "\n \t", 3); got == "" {
		t.Fatalf("delivered 仅空白但 raw 有字节应标注 lossy，got 空串")
	}
	clipped := "[output: ~9.0k tokens, clipped]\nxxx"
	if got := OutputIntegrityNote(strings.Repeat("x", 100), clipped, 100); got != "" {
		t.Fatalf("已有裁剪 marker 时 note = %q，want 空串", got)
	}
}

// TestOpSeqBothStatementsOutput 防止只执行第一句就因空输出判断提前返回的回归：
// 开关开启下，两个 echo 都必须出现在结果里。
func TestOpSeqBothStatementsOutput(t *testing.T) {
	SetOutputAccounting(true)
	defer SetOutputAccounting(false)
	t.Setenv("ARGENT_BUILTIN_SHELL", "1")

	got, err := runBashTool(t, "echo first; echo second")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("OpSeq 两语句未都输出：%q", got)
	}
	if strings.Contains(got, "(no output)") {
		t.Fatalf("非空输出被误判为空：%q", got)
	}
}
