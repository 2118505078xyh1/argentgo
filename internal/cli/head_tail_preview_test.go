package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/server"
)

func TestHeadTailPreview(t *testing.T) {
	numbered := func(n int) []string {
		out := make([]string, n)
		for i := 0; i < n; i++ {
			out[i] = fmt.Sprintf("line-%02d", i)
		}
		return out
	}
	cases := []struct {
		name  string
		lines []string
		head  int
		tail  int
		want  string
	}{
		{
			name:  "头尾各保留两行并标记隐藏段",
			lines: numbered(10),
			head:  2,
			tail:  2,
			want:  "line-00\nline-01\n... (6 lines hidden) ...\nline-08\nline-09",
		},
		{
			name:  "行数不足时原样返回",
			lines: numbered(3),
			head:  2,
			tail:  2,
			want:  "line-00\nline-01\nline-02",
		},
		{
			name:  "行数恰好等于头尾之和",
			lines: numbered(4),
			head:  2,
			tail:  2,
			want:  "line-00\nline-01\nline-02\nline-03",
		},
		{
			name:  "空输入返回空",
			lines: nil,
			head:  2,
			tail:  2,
			want:  "",
		},
		{
			name:  "头部为零",
			lines: numbered(6),
			head:  0,
			tail:  2,
			want:  "... (4 lines hidden) ...\nline-04\nline-05",
		},
		{
			name:  "尾部为零",
			lines: numbered(6),
			head:  2,
			tail:  0,
			want:  "line-00\nline-01\n... (4 lines hidden) ...",
		},
		{
			name:  "头尾都为零只留标记",
			lines: numbered(5),
			head:  0,
			tail:  0,
			want:  "... (5 lines hidden) ...",
		},
		{
			name:  "负数按零处理",
			lines: numbered(5),
			head:  -1,
			tail:  -2,
			want:  "... (5 lines hidden) ...",
		},
		{
			name:  "超出行数按行数处理",
			lines: numbered(3),
			head:  99,
			tail:  99,
			want:  "line-00\nline-01\nline-02",
		},
		{
			name:  "CJK 行完整保留",
			lines: []string{"开始 中文", "第二行 日本語", "第三行 한국어", "第四行 结束"},
			head:  1,
			tail:  1,
			want:  "开始 中文\n... (2 lines hidden) ...\n第四行 结束",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := headTailPreview(tc.lines, tc.head, tc.tail)
			if got != tc.want {
				t.Fatalf("headTailPreview = %q, want %q", got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("输出不是合法的 UTF-8: %q", got)
			}
		})
	}
}

func TestHeadTailPreviewKeepsWholeCJKLine(t *testing.T) {
	// 一行里含多字节字符；预览按行裁剪，绝不能在字符中间切断。
	line := strings.Repeat("中文", 40)
	got := headTailPreview([]string{line, "省略", "末尾"}, 1, 1)
	if got != line+"\n... (1 lines hidden) ...\n末尾" {
		t.Fatalf("CJK 行被破坏: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("输出不是合法的 UTF-8: %q", got)
	}
}

func TestSessionsHeadTailEnabled(t *testing.T) {
	// 策略变更后默认开启：未设置/空值/任意非假值都开启预览；
	// 只有 "0" 或 "false"（忽略大小写与首尾空白）关闭。
	cases := map[string]bool{
		"":        true,
		"0":       false,
		"false":   false,
		" FALSE ": false,
		"off":     true,
		"random":  true,
		"1":       true,
		"true":    true,
		"TRUE":    true,
		" yes ":   true,
		"On":      true,
	}
	for raw, want := range cases {
		stdio, _, _ := newTestIO("", map[string]string{headTailPreviewEnv: raw})
		if got := sessionsHeadTailEnabled(App{IO: stdio}); got != want {
			t.Errorf("sessionsHeadTailEnabled(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestSessionsShowHeadTailPreviewGating(t *testing.T) {
	events := make([]server.SessionEvent, 20)
	for i := range events {
		events[i] = server.SessionEvent{
			ID:   fmt.Sprintf("e%d", i),
			Kind: "text",
			Data: json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)),
		}
	}
	store := &fakeStore{events: map[string][]server.SessionEvent{"s1": events}}

	// 默认开启：保留头尾，中间被折叠，末行结果依然可见。
	stdio, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, Sessions: store}, []string{"sessions", "show", "s1"}); code != 0 {
		t.Fatalf("默认 exit = %d", code)
	}
	if !strings.Contains(out.String(), `{"n":19}`) {
		t.Fatalf("默认输出缺少末行: %q", out.String())
	}
	if !strings.Contains(out.String(), "... (4 lines hidden) ...") {
		t.Fatalf("默认输出缺少省略标记: %q", out.String())
	}

	// 显式关闭（=0）：输出保持完整，末尾结果仍在，且没有省略标记。
	stdio, out, _ = newTestIO("", map[string]string{headTailPreviewEnv: "0"})
	if code := RunWith(App{IO: stdio, Sessions: store}, []string{"sessions", "show", "s1"}); code != 0 {
		t.Fatalf("关闭预览 exit = %d", code)
	}
	if !strings.Contains(out.String(), `{"n":19}`) {
		t.Fatalf("关闭预览输出缺少末行: %q", out.String())
	}
	if strings.Contains(out.String(), "lines hidden") {
		t.Fatalf("关闭预览输出不应被截断: %q", out.String())
	}

	// 开启预览后依然遵守 broken pipe 不报错的约定。
	stdio, _, errOut := newTestIO("", map[string]string{headTailPreviewEnv: "1"})
	stdio.Out = errWriter{err: syscall.EPIPE}
	if code := RunWith(App{IO: stdio, Sessions: store}, []string{"sessions", "show", "s1"}); code != 0 {
		t.Fatalf("broken pipe exit = %d, want 0; stderr %q", code, errOut.String())
	}
}
