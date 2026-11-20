package skills

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseShellCommandsRecognizesExecutableInlineCode(t *testing.T) {
	body := "Run `!git status` then `!ls -la`."
	commands, spans := ParseShellCommands(body)
	want := []string{"git status", "ls -la"}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	if len(spans) != len(commands) {
		t.Fatalf("spans = %#v, want %d entries", spans, len(commands))
	}
	for i, span := range spans {
		if span.Start < 0 || span.End > len(body) || span.Start >= span.End {
			t.Errorf("span[%d] = %#v out of range", i, span)
			continue
		}
		token := body[span.Start:span.End]
		if !strings.HasSuffix(token, "`") || !strings.Contains(token, "!"+commands[i]) {
			t.Errorf("span[%d] = %q does not cover %q", i, token, commands[i])
		}
	}
}

func TestParseShellCommandsSkipsFencedCodeBlocks(t *testing.T) {
	body := "```sh\n`!echo inside`\n```\n`!echo outside`"
	commands, _ := ParseShellCommands(body)
	want := []string{"echo outside"}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestParseShellCommandsSkipsUnterminatedFence(t *testing.T) {
	body := "```sh\n`!echo never`"
	commands, _ := ParseShellCommands(body)
	if len(commands) != 0 {
		t.Fatalf("commands = %#v, want none", commands)
	}
}

func TestParseShellCommandsSkipsOrdinaryInlineCode(t *testing.T) {
	body := "Use `echo hi` and `!echo yes` and `echo !notexec`."
	commands, _ := ParseShellCommands(body)
	want := []string{"echo yes"}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestParseShellCommandsSkipsEscapedBackticks(t *testing.T) {
	body := "\\`!echo escaped\\` and `!echo real`"
	commands, _ := ParseShellCommands(body)
	want := []string{"echo real"}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestParseShellCommandsSkipsUnterminatedInlineCode(t *testing.T) {
	commands, _ := ParseShellCommands("`!echo never closed")
	if len(commands) != 0 {
		t.Fatalf("commands = %#v, want none", commands)
	}
}

func TestParseShellCommandsKeepsMultiLineCommand(t *testing.T) {
	body := "Try `!for i in a b; do\n  echo $i\ndone` now."
	wantCmd := "for i in a b; do\n  echo $i\ndone"
	commands, spans := ParseShellCommands(body)
	if !reflect.DeepEqual(commands, []string{wantCmd}) {
		t.Fatalf("commands = %#v, want [%q]", commands, wantCmd)
	}
	if len(spans) != 1 || body[spans[0].Start:spans[0].End] != "`!"+wantCmd+"`" {
		t.Errorf("span = %#v", spans)
	}
}

func TestParseShellCommandsDeduplicatesKeepingFirst(t *testing.T) {
	body := "`!same` then `!other` then `!same`"
	commands, spans := ParseShellCommands(body)
	want := []string{"same", "other"}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	if len(spans) != 2 {
		t.Fatalf("spans = %#v, want 2 entries", spans)
	}
	// The span kept for "same" is the first occurrence, not the last.
	if token := body[spans[0].Start:spans[0].End]; token != "`!same`" {
		t.Errorf("first span = %q", token)
	}
	if spans[0].Start > spans[1].Start {
		t.Errorf("spans out of order: %#v", spans)
	}
}

func TestParseShellCommandsCapsAtMax(t *testing.T) {
	var b strings.Builder
	for i := 0; i < MaxShellCommands+8; i++ {
		fmt.Fprintf(&b, "`!cmd%d` ", i)
	}
	commands, spans := ParseShellCommands(b.String())
	if len(commands) != MaxShellCommands {
		t.Fatalf("len = %d, want %d", len(commands), MaxShellCommands)
	}
	if len(spans) != MaxShellCommands {
		t.Errorf("spans = %d, want %d", len(spans), MaxShellCommands)
	}
	if commands[0] != "cmd0" || commands[MaxShellCommands-1] != fmt.Sprintf("cmd%d", MaxShellCommands-1) {
		t.Errorf("commands = %#v", commands)
	}
}

func TestParseShellCommandsIsCJKAndRuneSafe(t *testing.T) {
	body := "中文说明 `!echo 你好，世界` 结束 `!date`。"
	commands, spans := ParseShellCommands(body)
	want := []string{"echo 你好，世界", "date"}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	if len(spans) != 2 {
		t.Fatalf("spans = %#v, want 2 entries", spans)
	}
	if token := body[spans[0].Start:spans[0].End]; token != "`!echo 你好，世界`" {
		t.Errorf("first span = %q, want the full CJK token", token)
	}
}

func TestParseShellCommandsEmptyAndCommandFreeInput(t *testing.T) {
	for _, body := range []string{"", "   ", "no commands here", "`code only`", "！全角感叹号"} {
		commands, spans := ParseShellCommands(body)
		if len(commands) != 0 || len(spans) != 0 {
			t.Errorf("body %q: commands = %#v, spans = %#v, want none", body, commands, spans)
		}
	}
}

func TestShellInjectAllowed(t *testing.T) {
	cases := []struct {
		name       string
		trusted    bool
		killSwitch bool
		want       bool
	}{
		{"trusted and enabled", true, false, true},
		{"trusted but killed", true, true, false},
		{"untrusted but enabled", false, false, false},
		{"untrusted and killed", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShellInjectAllowed(tc.trusted, tc.killSwitch); got != tc.want {
				t.Errorf("ShellInjectAllowed(%v, %v) = %v, want %v", tc.trusted, tc.killSwitch, got, tc.want)
			}
		})
	}
}
