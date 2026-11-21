package app

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestPresentCommandNormalizes 覆盖 PresentCommand 对各字段的去首尾空白、换行计数
// 与空命令处理。断言整条结构体的精确字节，包括全角空白（U+3000）与 CRLF 的行为。
func TestPresentCommandNormalizes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		command     string
		workdir     string
		risk        string
		reason      string
		wantCommand string
		wantWorkdir string
		wantRisk    string
		wantReason  string
		wantLines   int
	}{
		{
			name:        "trims all fields",
			command:     "  go test ./...  ",
			workdir:     " /repo ",
			risk:        " high ",
			reason:      "  because  ",
			wantCommand: "go test ./...",
			wantWorkdir: "/repo",
			wantRisk:    "high",
			wantReason:  "because",
			wantLines:   1,
		},
		{
			name:        "empty command still occupies one line",
			command:     "   ",
			workdir:     "\t",
			risk:        "",
			reason:      "  ",
			wantCommand: "",
			wantWorkdir: "",
			wantRisk:    "",
			wantReason:  "",
			wantLines:   1,
		},
		{
			name:        "multiline command counts lines",
			command:     "line1\nline2\nline3",
			wantCommand: "line1\nline2\nline3",
			wantLines:   3,
		},
		{
			name:        "CRLF counts newlines",
			command:     "a\r\nb\r\nc",
			wantCommand: "a\r\nb\r\nc",
			wantLines:   3,
		},
		{
			name:        "fullwidth spaces are trimmed",
			command:     "\tcmd\t",
			workdir:     "　/repo　", // U+3000 全角空格
			risk:        "　中危　",
			reason:      "　更改核心　",
			wantCommand: "cmd",
			wantWorkdir: "/repo",
			wantRisk:    "中危",
			wantReason:  "更改核心",
			wantLines:   1,
		},
		{
			name:        "CJK reason kept when short",
			command:     "删除文件",
			reason:      "  因为改动了核心文件  ",
			wantCommand: "删除文件",
			wantReason:  "因为改动了核心文件",
			wantLines:   1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := PresentCommand(tc.command, tc.workdir, tc.risk, tc.reason)
			if got.Command != tc.wantCommand {
				t.Errorf("Command = %q, want %q", got.Command, tc.wantCommand)
			}
			if got.Workdir != tc.wantWorkdir {
				t.Errorf("Workdir = %q, want %q", got.Workdir, tc.wantWorkdir)
			}
			if got.Risk != tc.wantRisk {
				t.Errorf("Risk = %q, want %q", got.Risk, tc.wantRisk)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if got.Lines != tc.wantLines {
				t.Errorf("Lines = %d, want %d", got.Lines, tc.wantLines)
			}
		})
	}
}

// TestPresentCommandDoesNotModifyInputs 保证 PresentCommand 是纯函数：调用前后四个
// 入参变量保持原样（Go 字符串不可变，这里显式固定这一契约，防止未来误改为指针）。
func TestPresentCommandDoesNotModifyInputs(t *testing.T) {
	command, workdir, risk, reason := "  go build  ", " /app ", " high ", "  reason  "
	_ = PresentCommand(command, workdir, risk, reason)

	if command != "  go build  " || workdir != " /app " || risk != " high " || reason != "  reason  " {
		t.Fatalf("inputs mutated: command=%q workdir=%q risk=%q reason=%q",
			command, workdir, risk, reason)
	}
}

// TestPresentCommandReasonTruncation 覆盖 reason 的 rune 安全有界截断：恰好等于上限
// 时原样保留；超出时保留前 limit 个 rune 并追加省略号；结果始终是合法 UTF-8，
// 且不含替换字符（半个汉字）。
func TestPresentCommandReasonTruncation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reason    string
		wantRunes int // 截断后期望的 rune 数
		wantTail  string
	}{
		{
			name:      "ascii under limit unchanged",
			reason:    strings.Repeat("a", commandPresentationReasonLimit-1),
			wantRunes: commandPresentationReasonLimit - 1,
			wantTail:  "a",
		},
		{
			name:      "ascii exactly at limit unchanged",
			reason:    strings.Repeat("a", commandPresentationReasonLimit),
			wantRunes: commandPresentationReasonLimit,
			wantTail:  "a",
		},
		{
			name:      "ascii over limit truncated",
			reason:    strings.Repeat("a", commandPresentationReasonLimit+50),
			wantRunes: commandPresentationReasonLimit + 1, // limit 个 rune + 省略号
			wantTail:  commandPresentationEllipsis,
		},
		{
			name:      "CJK exactly at limit unchanged",
			reason:    strings.Repeat("中", commandPresentationReasonLimit),
			wantRunes: commandPresentationReasonLimit,
			wantTail:  "中",
		},
		{
			name:      "CJK over limit truncated safely",
			reason:    strings.Repeat("中", commandPresentationReasonLimit+50),
			wantRunes: commandPresentationReasonLimit + 1,
			wantTail:  commandPresentationEllipsis,
		},
		{
			name:      "mixed CJK and ascii over limit",
			reason:    strings.Repeat("中a", commandPresentationReasonLimit+10),
			wantRunes: commandPresentationReasonLimit + 1,
			wantTail:  commandPresentationEllipsis,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := PresentCommand("cmd", "", "", tc.reason)
			if !utf8.ValidString(got.Reason) {
				t.Fatalf("Reason is not valid UTF-8: %q", got.Reason)
			}
			if strings.ContainsRune(got.Reason, utf8.RuneError) {
				t.Fatalf("Reason contains replacement rune: %q", got.Reason)
			}
			if n := utf8.RuneCountInString(got.Reason); n != tc.wantRunes {
				t.Errorf("Reason rune count = %d, want %d", n, tc.wantRunes)
			}
			if !strings.HasSuffix(got.Reason, tc.wantTail) {
				t.Errorf("Reason = %q, want suffix %q", got.Reason, tc.wantTail)
			}
			// 超过上限时必须出现省略号；未超过时绝不能出现。
			wantEllipsis := utf8.RuneCountInString(tc.reason) > commandPresentationReasonLimit
			if gotEllipsis := strings.Contains(got.Reason, commandPresentationEllipsis); gotEllipsis != wantEllipsis {
				t.Errorf("Reason contains ellipsis = %v, want %v", gotEllipsis, wantEllipsis)
			}
		})
	}
}

// TestCommandPresentationSummary 断言 Summary 的精确字节：风险标记、命令预览、
// 目录与可选原因段，以及风险为空时的“未标注”与多行命令被折叠为单行。
func TestCommandPresentationSummary(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    CommandPresentation
		want string
	}{
		{
			name: "all fields",
			p:    PresentCommand("go test ./...", "/repo", "high", "因为改动"),
			want: "【命令审批】风险：high｜命令：go test ./...｜目录：/repo｜原因：因为改动",
		},
		{
			name: "risk empty shows unknown and omits optional segments",
			p:    PresentCommand("ls", "", "", ""),
			want: "【命令审批】风险：未标注｜命令：ls",
		},
		{
			name: "multiline command flattened to one line",
			p:    PresentCommand("echo a\n echo   b", "", "low", ""),
			want: "【命令审批】风险：low｜命令：echo a echo b",
		},
		{
			name: "CJK risk and command",
			p:    PresentCommand("删除 文件", "", "中危", ""),
			want: "【命令审批】风险：中危｜命令：删除 文件",
		},
		{
			name: "empty command and whitespace reason",
			p:    PresentCommand("  ", " /app ", "  ", "  "),
			want: "【命令审批】风险：未标注｜命令：｜目录：/app",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Summary(); got != tc.want {
				t.Fatalf("Summary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCommandPresentationSummaryStableAndBounded 保证 Summary 稳定（多次调用一致）
// 且有界：超长命令的预览被截断到上限 + 省略号，结果仍是合法 UTF-8。
func TestCommandPresentationSummaryStableAndBounded(t *testing.T) {
	p := PresentCommand(strings.Repeat("长", 500), "/workdir", "high", strings.Repeat("因", 500))

	first := p.Summary()
	if second := p.Summary(); second != first {
		t.Fatalf("Summary() not stable: %q != %q", second, first)
	}
	if !utf8.ValidString(first) {
		t.Fatalf("Summary() is not valid UTF-8: %q", first)
	}

	// 取出“命令：”段，校验预览长度有界且以省略号结尾。
	var preview string
	for _, seg := range strings.Split(first, commandPresentationSeparator) {
		if strings.HasPrefix(seg, commandPresentationCommandLabel+"：") {
			preview = strings.TrimPrefix(seg, commandPresentationCommandLabel+"：")
			break
		}
	}
	if preview == "" {
		t.Fatalf("Summary() has no command segment: %q", first)
	}
	if n := utf8.RuneCountInString(preview); n != commandPresentationPreviewLimit+1 {
		t.Errorf("command preview rune count = %d, want %d", n, commandPresentationPreviewLimit+1)
	}
	if !strings.HasSuffix(preview, commandPresentationEllipsis) {
		t.Errorf("command preview = %q, want trailing ellipsis", preview)
	}
}

// TestRedactCommandSecrets 覆盖 RedactCommandSecrets 的脱敏：普通命令原样保留，
// KEY=xxx / token / sk- 形状的密钥被 security.Redact 替换为占位符，不可见 Unicode
// Tag 字符被剥离，且不修改入参。
func TestRedactCommandSecrets(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain command unchanged",
			in:   "go test ./...",
			want: "go test ./...",
		},
		{
			name: "api key value redacted",
			in:   "export API_KEY=supersecret123",
			want: "export API_KEY=[redacted:api key]",
		},
		{
			name: "token value redacted",
			in:   "GITHUB_TOKEN=mypass123",
			want: "GITHUB_TOKEN=[redacted:token]",
		},
		{
			name: "password value redacted",
			in:   "PASSWORD=abc123",
			want: "PASSWORD=[redacted:password]",
		},
		{
			name: "sk prefix redacted",
			in:   "sk-proj-abcdefghijklmnop",
			want: "[redacted:api key]",
		},
		{
			name: "unicode tags stripped",
			in:   "echo \U000E0001hidden",
			want: "echo hidden",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactCommandSecrets(tc.in); got != tc.want {
				t.Fatalf("RedactCommandSecrets(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRedactCommandSecretsDoesNotModifyInput 保证脱敏是纯函数，入参变量不被改写。
func TestRedactCommandSecretsDoesNotModifyInput(t *testing.T) {
	command := "API_KEY=supersecret123"
	_ = RedactCommandSecrets(command)
	if command != "API_KEY=supersecret123" {
		t.Fatalf("input mutated: %q", command)
	}
}
