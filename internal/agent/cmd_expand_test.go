package agent

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExpandCommandTable(t *testing.T) {
	vars := map[string]string{
		"NAME":  "世界",
		"PATH":  "/bin",
		"EMPTY": "",
	}

	// 命令替换回调：把命令映射为固定输出，用于验证 $(cmd) 的替换与括号配平。
	cmdOut := map[string]string{
		"echo hi":       "hi",
		"pwd":           "/work",
		"echo (a)":      "paren",
		"echo $(inner)": "nested-ok",
	}
	callback := func(cmd string) (string, error) {
		return cmdOut[cmd], nil
	}
	failing := func(cmd string) (string, error) {
		return "", errors.New("boom")
	}

	tests := []struct {
		name     string
		req      ExpansionRequest
		want     string
		wantErr  error
		wantText string // 可选：出错时仍希望保留的可读输出
	}{
		{
			name: "bare variable",
			req:  ExpansionRequest{Text: "hello $NAME", Vars: vars},
			want: "hello 世界",
		},
		{
			name: "braced variable",
			req:  ExpansionRequest{Text: "${PATH}/ls", Vars: vars},
			want: "/bin/ls",
		},
		{
			name: "missing variable becomes empty",
			req:  ExpansionRequest{Text: "a${NOPE}b $MISSING c", Vars: vars},
			want: "ab  c",
		},
		{
			name: "empty variable value",
			req:  ExpansionRequest{Text: "x$EMPTY y", Vars: vars},
			want: "x y",
		},
		{
			name: "dollar question uses last status",
			req:  ExpansionRequest{Text: "code=$?", LastStatus: 42},
			want: "code=42",
		},
		{
			name: "negative last status",
			req:  ExpansionRequest{Text: "$?", LastStatus: -1},
			want: "-1",
		},
		{
			name: "command substitution",
			req:  ExpansionRequest{Text: "dir=$(pwd)", CommandOutput: callback},
			want: "dir=/work",
		},
		{
			name: "command substitution with parens in argument",
			req:  ExpansionRequest{Text: "$(echo (a))!", CommandOutput: callback},
			want: "paren!",
		},
		{
			name: "nested command simplification: raw cmd passed to callback",
			req:  ExpansionRequest{Text: "$(echo $(inner))", CommandOutput: callback},
			want: "nested-ok",
		},
		{
			name: "nil callback keeps command literal",
			req:  ExpansionRequest{Text: "x=$(echo hi)y"},
			want: "x=$(echo hi)y",
		},
		{
			name: "unclosed command kept literal",
			req:  ExpansionRequest{Text: "x=$(echo hi", CommandOutput: callback},
			want: "x=$(echo hi",
		},
		{
			name: "unclosed brace kept literal",
			req:  ExpansionRequest{Text: "x=${NAME", Vars: vars},
			want: "x=${NAME",
		},
		{
			name: "single quotes suppress expansion",
			req:  ExpansionRequest{Text: "'$NAME' \"$NAME\" $NAME", Vars: vars, CommandOutput: callback},
			want: "'$NAME' \"世界\" 世界",
		},
		{
			name: "single quotes treat backslash literally",
			req:  ExpansionRequest{Text: `'$NAME \$NAME'`, Vars: vars},
			want: `'$NAME \$NAME'`,
		},
		{
			name: "double quotes still expand",
			req:  ExpansionRequest{Text: `"$NAME $?" "$(pwd)"`, Vars: vars, LastStatus: 7, CommandOutput: callback},
			want: `"世界 7" "/work"`,
		},
		{
			name: "apostrophe inside double quotes does not start single quote",
			req:  ExpansionRequest{Text: `"it's $NAME"`, Vars: vars},
			want: `"it's 世界"`,
		},
		{
			name: "backslash escapes dollar",
			req:  ExpansionRequest{Text: `\$NAME $NAME`, Vars: vars},
			want: `$NAME 世界`,
		},
		{
			name: "backslash escapes backslash",
			req:  ExpansionRequest{Text: `\\$NAME`, Vars: vars},
			want: `\世界`,
		},
		{
			name: "backslash escapes quote",
			req:  ExpansionRequest{Text: `"a\"b" $NAME`, Vars: vars},
			want: `"a"b" 世界`,
		},
		{
			name: "backslash escapes question and parens",
			req:  ExpansionRequest{Text: `\? \$? \( $?`, LastStatus: 3},
			want: `? $? ( 3`,
		},
		{
			name: "trailing backslash kept",
			req:  ExpansionRequest{Text: `abc\`, Vars: vars},
			want: `abc\`,
		},
		{
			name: "CJK and mixed expansions",
			req:  ExpansionRequest{Text: `中文$NAME$(echo hi)结束`, Vars: vars, CommandOutput: callback},
			want: "中文世界hi结束",
		},
		{
			name: "CJK variable name via braces",
			req:  ExpansionRequest{Text: `${名字}`, Vars: map[string]string{"名字": "张三"}},
			want: "张三",
		},
		{
			name:     "callback error preserves literal and returns sentinel",
			req:      ExpansionRequest{Text: "a=$(echo hi)b=$NAME", Vars: vars, CommandOutput: failing},
			want:     "a=$(echo hi)b=世界",
			wantErr:  ErrCommandOutput,
			wantText: "a=$(echo hi)b=世界",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandCommand(tt.req)
			if got != tt.want {
				t.Errorf("ExpandCommand() = %q, want %q", got, tt.want)
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("err = %v, want errors.Is(_, %v)", err, tt.wantErr)
				}
				if tt.wantText != "" && got != tt.wantText {
					t.Errorf("output on error = %q, want %q", got, tt.wantText)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestExpandCommandDoesNotMutateInput(t *testing.T) {
	vars := map[string]string{"NAME": "世界", "PATH": "/bin"}
	original := map[string]string{"NAME": "世界", "PATH": "/bin"}
	req := ExpansionRequest{
		Text:          "$NAME ${PATH} $(echo hi) $?",
		Vars:          vars,
		LastStatus:    5,
		CommandOutput: func(string) (string, error) { return "ok", nil },
	}

	if _, err := ExpandCommand(req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(vars, original) {
		t.Fatalf("Vars mutated: got %v, want %v", vars, original)
	}
	// req 的其他字段同样是值语义，调用后应保持不变。
	if req.Text != "$NAME ${PATH} $(echo hi) $?" || req.LastStatus != 5 || req.CommandOutput == nil {
		t.Fatalf("req mutated: %+v", req)
	}
}

func TestExpandCommandTruncatesAtRuneBoundary(t *testing.T) {
	huge := strings.Repeat("汉", maxExpandedOutput) // 每字 3 字节，远超上限
	req := ExpansionRequest{
		Text:          "$(big)",
		CommandOutput: func(string) (string, error) { return huge, nil },
	}

	got, err := ExpandCommand(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) > maxExpandedOutput {
		t.Errorf("len=%d exceeds limit %d", len(got), maxExpandedOutput)
	}
	if !utf8.ValidString(got) {
		t.Errorf("output is not valid UTF-8: %q", got)
	}
	if len(got)%3 != 0 {
		t.Errorf("output length %d is not a multiple of the 3-byte CJK rune", len(got))
	}
	if got == huge {
		t.Error("output was not truncated")
	}
}

func TestExpandCommandCapsTotalAcrossManyExpansions(t *testing.T) {
	// 多个片段各自不超上限，但总和会超过；最终输出仍应受 maxExpandedOutput 限制。
	chunk := strings.Repeat("a", 1024)
	req := ExpansionRequest{
		Text: strings.Repeat("$V", 200),
		Vars: map[string]string{"V": chunk},
	}
	got, err := ExpandCommand(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != maxExpandedOutput {
		t.Errorf("len=%d, want %d", len(got), maxExpandedOutput)
	}
	if !utf8.ValidString(got) {
		t.Error("output is not valid UTF-8")
	}
}
