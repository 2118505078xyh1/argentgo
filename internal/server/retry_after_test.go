package server

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

// TestParseRetryAfterSeconds 覆盖增量秒数形态：合法值、前后空白、
// 恰好等于上限、超大钳制、正整数溢出钳制，以及各种回退为无提示的形态。
func TestParseRetryAfterSeconds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		header  string
		wantSec int
		wantOK  bool
	}{
		{"合法秒数", "120", 120, true},
		{"前后空白", "  45  ", 45, true},
		{"带正号", "+30", 30, true},
		{"一秒", "1", 1, true},
		{"恰好等于上限", strconv.Itoa(maxRetryAfterSeconds), maxRetryAfterSeconds, true},
		{"超过上限被钳制", strconv.Itoa(maxRetryAfterSeconds + 1), maxRetryAfterSeconds, true},
		{"远超上限被钳制", "999999999", maxRetryAfterSeconds, true},
		{"正整数范围溢出被钳制", "999999999999999999999999999", maxRetryAfterSeconds, true},
		{"零回退", "0", 0, false},
		{"负值回退", "-5", 0, false},
		{"负整数范围溢出回退", "-999999999999999999999999999", 0, false},
		{"空回退", "", 0, false},
		{"仅空白回退", " \t ", 0, false},
		{"非数字回退", "abc", 0, false},
		{"小数回退", "1.5", 0, false},
		{"单位后缀回退", "1s", 0, false},
		{"CJK 回退", "稍后重试", 0, false},
		{"CJK 后缀回退", "100秒", 0, false},
		{"全角数字回退", "１２０", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRetryAfter(tc.header)
			if got != tc.wantSec || ok != tc.wantOK {
				t.Fatalf("parseRetryAfter(%q) = (%d, %v), want (%d, %v)", tc.header, got, ok, tc.wantSec, tc.wantOK)
			}
		})
	}
}

// TestParseRetryAfterHTTPDate 用固定时钟验证 HTTP-date 形态：
// 正向间隔向上取整、超过上限钳制、已过期与非法日期回退。
func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, time.October, 21, 7, 0, 0, 0, time.UTC)
	// 带亚秒分量的时钟，用于覆盖「剩余不足一秒」的向上取整分支。
	nowWithMillis := now.Add(500 * time.Millisecond)
	for _, tc := range []struct {
		name    string
		header  string
		at      time.Time
		wantSec int
		wantOK  bool
	}{
		// RFC1123：30 秒后。
		{"RFC1123 日期", "Wed, 21 Oct 2026 07:00:30 GMT", now, 30, true},
		// 剩余 0.5 秒也向上取整为 1 秒。
		{"不足一秒向上取整", "Wed, 21 Oct 2026 07:00:01 GMT", nowWithMillis, 1, true},
		// 超过上限的日期钳制到 15 分钟。
		{"超长日期钳制", "Wed, 21 Oct 2026 09:00:00 GMT", now, maxRetryAfterSeconds, true},
		// RFC850 形式（时区名为 GMT）。
		{"RFC850 日期", "Wednesday, 21-Oct-26 07:00:30 GMT", now, 30, true},
		// ANSIC 形式同样被 http.ParseTime 接受。
		{"ANSIC 日期", "Wed Oct 21 07:00:30 2026", now, 30, true},
		// 已过期日期不构成有效提示。
		{"已过期日期回退", "Wed, 21 Oct 2026 06:59:00 GMT", now, 0, false},
		// CJK 文本既非秒数也非日期。
		{"CJK 回退", "稍后重试", now, 0, false},
		// 完全非法文本。
		{"非法日期回退", "not-a-date", now, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRetryAfterAt(tc.header, tc.at)
			if got != tc.wantSec || ok != tc.wantOK {
				t.Fatalf("parseRetryAfterAt(%q) = (%d, %v), want (%d, %v)", tc.header, got, ok, tc.wantSec, tc.wantOK)
			}
		})
	}
}

// TestParseRetryAfterHTTPDateViaPublicEntry 验证对外入口 parseRetryAfter
// 同样能解析 HTTP-date；未来日期解析成功且不超过上限。
func TestParseRetryAfterHTTPDateViaPublicEntry(t *testing.T) {
	future := time.Now().Add(2 * time.Hour).UTC().Format(http.TimeFormat)
	got, ok := parseRetryAfter(future)
	if !ok {
		t.Fatalf("parseRetryAfter(%q) 应解析成功", future)
	}
	if got <= 0 || got > maxRetryAfterSeconds {
		t.Fatalf("parseRetryAfter(%q) = %d, 应落在 (0, %d]", future, got, maxRetryAfterSeconds)
	}
}

// TestParseRetryAfterAtSecondsIgnoresClock 验证增量秒数分支与时钟无关，
// 注入不同 now 得到一致结果。
func TestParseRetryAfterAtSecondsIgnoresClock(t *testing.T) {
	got, ok := parseRetryAfterAt("42", time.Time{})
	if !ok || got != 42 {
		t.Fatalf("parseRetryAfterAt(42) = (%d, %v), want (42, true)", got, ok)
	}
}
