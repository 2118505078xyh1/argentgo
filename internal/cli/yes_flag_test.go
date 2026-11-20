package cli

import (
	"io"
	"strings"
	"testing"
)

// TestYesFlagDefaultsFalse 验证不传 --yes 时所有现有行为保持不变：
// 零值 false 表示"未设置"，不会触发任何自动确认。
func TestYesFlagDefaultsFalse(t *testing.T) {
	cases := [][]string{
		nil,
		{"hello"},
		{"--yolo", "hello"},
		{"--model", "gpt-4o-mini", "hello"},
	}
	for _, args := range cases {
		opts, _, err := parseArgs(args, func(string) string { return "" }, io.Discard)
		if err != nil {
			t.Fatalf("parseArgs(%q): %v", args, err)
		}
		if opts.Yes {
			t.Fatalf("parseArgs(%q).Yes = true, want false", args)
		}
	}
}

// TestYesFlagParsing 验证 --yes 的各种合法写法都能解析为 true，
// 显式的 --yes=false 则保持 false，且不影响位置参数。
func TestYesFlagParsing(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		want   bool
		prompt string
	}{
		{"bare flag", []string{"--yes", "hello"}, true, "hello"},
		{"explicit true", []string{"--yes=true", "hello"}, true, "hello"},
		{"explicit one", []string{"--yes=1", "hello"}, true, "hello"},
		{"explicit false", []string{"--yes=false", "hello"}, false, "hello"},
		{"interleaved", []string{"hello", "--yes", "world"}, true, "hello world"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, positional, err := parseArgs(tc.args, func(string) string { return "" }, io.Discard)
			if err != nil {
				t.Fatalf("parseArgs(%q): %v", tc.args, err)
			}
			if opts.Yes != tc.want {
				t.Fatalf("parseArgs(%q).Yes = %v, want %v", tc.args, opts.Yes, tc.want)
			}
			if got := strings.Join(positional, " "); got != tc.prompt {
				t.Fatalf("positional = %q, want %q", got, tc.prompt)
			}
		})
	}
}

// TestYesFlagRejectsInvalidValue 验证 --yes=<非法布尔值> 会被 flag 包拒绝，
// 返回 usage 错误且不产出 Options，而不是静默当成 false 继续运行。
func TestYesFlagRejectsInvalidValue(t *testing.T) {
	for _, value := range []string{"maybe", "yes", "2", ""} {
		args := []string{"--yes=" + value}
		opts, _, err := parseArgs(args, func(string) string { return "" }, io.Discard)
		if err == nil {
			t.Fatalf("parseArgs(%q) unexpectedly succeeded: %+v", args, opts)
		}
		if opts != nil {
			t.Fatalf("parseArgs(%q) returned options on error: %+v", args, opts)
		}
		if !strings.Contains(err.Error(), "invalid boolean value") {
			t.Fatalf("parseArgs(%q) error = %q, want invalid boolean value", args, err.Error())
		}
	}
}

// TestYesFlagCoexistsWithOtherFlags 验证 --yes 与其它标志共存时各自独立生效，
// 既不吞掉相邻 token，也不改变其它字段的解析结果。
func TestYesFlagCoexistsWithOtherFlags(t *testing.T) {
	opts, positional, err := parseArgs(
		[]string{"--model", "gpt-4o-mini", "--yes", "--max-turns", "7", "--yolo", "--no-color", "do it"},
		func(string) string { return "" },
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Yes {
		t.Fatal("Yes = false, want true")
	}
	if opts.Model != "gpt-4o-mini" || opts.MaxTurns != 7 || !opts.Yolo || !opts.NoColor {
		t.Fatalf("other flags changed: %+v", opts)
	}
	if got := strings.Join(positional, " "); got != "do it" {
		t.Fatalf("positional = %q, want %q", got, "do it")
	}
}
