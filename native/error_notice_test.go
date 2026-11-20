package main

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

func TestCollapseEngineErrorSpace(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "sse reset", "sse reset"},
		{"mixed whitespace", "sse \t reset\n again  \r\n", "sse reset again"},
		{"unicode whitespace", "错误\u3000详情", "错误 详情"},
		{"empty", "  \n\t ", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := collapseEngineErrorSpace(tc.in); got != tc.want {
				t.Fatalf("collapseEngineErrorSpace(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestClipErrorRunesBoundsWithoutSplittingUTF8(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short", "boom", 10, "boom"},
		{"exact", "12345", 5, "12345"},
		{"one over", "123456", 5, "1234…"},
		{"multibyte", strings.Repeat("字", 10), 5, "字字字字…"},
		{"non positive max", "boom", 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := clipErrorRunes(tc.in, tc.max)
			if got != tc.want {
				t.Fatalf("clipErrorRunes(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if utf8.RuneCountInString(got) > tc.max {
				t.Fatalf("rune count = %d, want <= %d (%q)", utf8.RuneCountInString(got), tc.max, got)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("clip split a UTF-8 sequence: %q", got)
			}
		})
	}
}

func TestEngineErrorNoticeRedactsCredentials(t *testing.T) {
	detail := "sse reset key=sk-proj-abcdefghijklmnop api_key: xyz1234567890"
	got := engineErrorNotice(errors.New(detail))
	for _, secret := range []string{"sk-proj-abcdefghijklmnop", "xyz1234567890"} {
		if strings.Contains(got, secret) {
			t.Fatalf("credential leaked into notice: %q", got)
		}
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("redaction marker missing: %q", got)
	}
	if !strings.HasPrefix(got, strings.TrimSpace(engineErrorPrefix)) {
		t.Fatalf("notice missing error banner: %q", got)
	}
}

func TestEngineErrorNoticeIsBoundedSingleLine(t *testing.T) {
	// A hostile or malformed producer can hand us a very long, multi-line
	// detail; the transcript must stay one line and of bounded size.
	detail := strings.Repeat("字行\n\t", maxEngineErrorDetail)
	got := engineErrorNotice(errors.New(detail))
	if strings.ContainsAny(got, "\r\n\t") {
		t.Fatalf("notice contains whitespace from the error: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != utf8.RuneCountInString(engineErrorPrefix)+maxEngineErrorDetail {
		t.Fatalf("rune count = %d, want %d", n, utf8.RuneCountInString(engineErrorPrefix)+maxEngineErrorDetail)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("long detail not marked truncated: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatal("notice split a UTF-8 sequence")
	}
}

func TestEngineErrorNoticeEmptyErrorStillShowsBanner(t *testing.T) {
	for _, err := range []error{nil, errors.New(" \n ")} {
		got := engineErrorNotice(err)
		if got != strings.TrimSpace(engineErrorPrefix) {
			t.Fatalf("notice = %q for %v, want %q", got, err, strings.TrimSpace(engineErrorPrefix))
		}
	}
}

func TestFinishEngineErrorKeepsReplyVisibleAndStatusExplicit(t *testing.T) {
	app := newTestChat()
	reply := &message{id: 1, kind: msgAssistant}
	app.messages = append(app.messages, reply)
	app.reply = reply
	app.streaming = true

	app.finishEngineError(errors.New("boom\nsecond line"))

	if !reply.done || app.streaming || app.reply != nil {
		t.Fatalf("reply not finalized: done=%v streaming=%v reply=%v", reply.done, app.streaming, app.reply)
	}
	if reply.text != engineErrorPrefix+"boom second line" {
		t.Fatalf("reply text = %q", reply.text)
	}
	if app.status != "引擎错误" {
		t.Fatalf("status = %q, want 引擎错误", app.status)
	}

	tt := ui.NewTester(app.view, 1180, 720)
	if !tt.HasText("引擎错误") || !tt.HasText("boom second line") {
		t.Fatalf("error banner not visible in %q", tt.Texts())
	}
}
