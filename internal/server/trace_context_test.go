package server

import "testing"

// 测试中使用的合法 ID 取自 W3C Trace Context 规范示例。
const (
	testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	testSpanID  = "00f067aa0ba902b7"
)

func TestParseTraceparentValid(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{"采样标志置位", "00-" + testTraceID + "-" + testSpanID + "-01"},
		{"采样标志清零", "00-" + testTraceID + "-" + testSpanID + "-00"},
		{"其余标志位不影响解析", "00-" + testTraceID + "-" + testSpanID + "-ff"},
		{"大写十六进制", "00-4BF92F3577B34DA6A3CE929D0E0E4736-00F067AA0BA902B7-01"},
		{"首尾空白被忽略", "  00-" + testTraceID + "-" + testSpanID + "-01\t"},
		{"更高版本合法", "01-" + testTraceID + "-" + testSpanID + "-01"},
		{"更高版本忽略附加段", "02-" + testTraceID + "-" + testSpanID + "-01-extra-field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTrace, gotSpan, ok := parseTraceparent(tc.header)
			if !ok {
				t.Fatalf("parseTraceparent(%q) 应成功，实际失败", tc.header)
			}
			if gotTrace != testTraceID {
				t.Errorf("traceID = %q，期望 %q（规范小写形式）", gotTrace, testTraceID)
			}
			if gotSpan != testSpanID {
				t.Errorf("spanID = %q，期望 %q（规范小写形式）", gotSpan, testSpanID)
			}
		})
	}
}

func TestParseTraceparentInvalid(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{"空字符串", ""},
		{"仅空白", "   \t"},
		{"段数不足", "00-" + testTraceID + "-" + testSpanID},
		{"traceID 长度过短", "00-4bf92f3577b34da6a3ce929d0e0e473-" + testSpanID + "-01"},
		{"traceID 长度过长", "00-" + testTraceID + "0-" + testSpanID + "-01"},
		{"spanID 长度过短", "00-" + testTraceID + "-00f067aa0ba902b-01"},
		{"flags 长度为一", "00-" + testTraceID + "-" + testSpanID + "-0"},
		{"flags 长度过长", "00-" + testTraceID + "-" + testSpanID + "-001"},
		{"traceID 全零", "00-00000000000000000000000000000000-" + testSpanID + "-01"},
		{"spanID 全零", "00-" + testTraceID + "-0000000000000000-01"},
		{"traceID 含非十六进制字符", "00-4bf92f3577b34da6a3ce929d0e0e473g-" + testSpanID + "-01"},
		{"flags 含非十六进制字符", "00-" + testTraceID + "-" + testSpanID + "-0z"},
		{"版本长度为一", "0-" + testTraceID + "-" + testSpanID + "-01"},
		{"版本长度为三", "000-" + testTraceID + "-" + testSpanID + "-01"},
		{"版本非法字符", "0g-" + testTraceID + "-" + testSpanID + "-01"},
		{"版本 ff 保留", "ff-" + testTraceID + "-" + testSpanID + "-01"},
		{"版本 FF 保留（大小写不敏感）", "FF-" + testTraceID + "-" + testSpanID + "-01"},
		{"版本 00 不允许附加段", "00-" + testTraceID + "-" + testSpanID + "-01-extra"},
		{"中段含空格", "00-" + testTraceID + "- 00f067aa0ba902b7 -01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := parseTraceparent(tc.header); ok {
				t.Errorf("parseTraceparent(%q) 应失败，实际成功", tc.header)
			}
		})
	}
}

func TestParseTraceparentCJK(t *testing.T) {
	// CJK 字符及其全角数字都不是合法十六进制，必须直接判为非法。
	cases := []struct {
		name   string
		header string
	}{
		{"traceID 中出现汉字", "00-追踪f92f3577b34da6a3ce929d0e0e4736-" + testSpanID + "-01"},
		{"spanID 中出现汉字", "00-" + testTraceID + "-00f067aa0ba902b字-01"},
		{"版本使用全角数字", "００-" + testTraceID + "-" + testSpanID + "-01"},
		{"flags 使用全角数字", "00-" + testTraceID + "-" + testSpanID + "-０１"},
		{"前缀含汉字", "追踪00-" + testTraceID + "-" + testSpanID + "-01"},
		{"后缀含汉字", "00-" + testTraceID + "-" + testSpanID + "-01追踪"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := parseTraceparent(tc.header); ok {
				t.Errorf("parseTraceparent(%q) 含 CJK 应失败，实际成功", tc.header)
			}
		})
	}
}

func TestFormatTraceparent(t *testing.T) {
	cases := []struct {
		name    string
		traceID string
		spanID  string
		sampled bool
		want    string
	}{
		{"已采样", testTraceID, testSpanID, true, "00-" + testTraceID + "-" + testSpanID + "-01"},
		{"未采样", testTraceID, testSpanID, false, "00-" + testTraceID + "-" + testSpanID + "-00"},
		{"大写输入归一化为小写", "4BF92F3577B34DA6A3CE929D0E0E4736", "00F067AA0BA902B7", true, "00-" + testTraceID + "-" + testSpanID + "-01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatTraceparent(tc.traceID, tc.spanID, tc.sampled); got != tc.want {
				t.Errorf("formatTraceparent() = %q，期望 %q", got, tc.want)
			}
		})
	}
}

func TestFormatTraceparentInvalidInput(t *testing.T) {
	cases := []struct {
		name    string
		traceID string
		spanID  string
	}{
		{"traceID 过短", testTraceID[:31], testSpanID},
		{"traceID 全零", "00000000000000000000000000000000", testSpanID},
		{"traceID 含汉字", "4bf92f3577b34da6a3ce929d0e0e473汉", testSpanID},
		{"spanID 过短", testTraceID, testSpanID[:15]},
		{"spanID 全零", testTraceID, "0000000000000000"},
		{"spanID 含汉字", testTraceID, "00f067aa0ba902b字"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatTraceparent(tc.traceID, tc.spanID, true); got != "" {
				t.Errorf("formatTraceparent() = %q，非法输入应返回空字符串", got)
			}
		})
	}
}

func TestTraceparentRoundTrip(t *testing.T) {
	for _, sampled := range []bool{true, false} {
		header := formatTraceparent(testTraceID, testSpanID, sampled)
		gotTrace, gotSpan, ok := parseTraceparent(header)
		if !ok {
			t.Fatalf("生成值 %q 应可被解析", header)
		}
		if gotTrace != testTraceID || gotSpan != testSpanID {
			t.Errorf("往返后 ID = (%q, %q)，期望 (%q, %q)", gotTrace, gotSpan, testTraceID, testSpanID)
		}
	}

	// 解析大写头后再生成，应得到规范的小写头。
	upper := "00-4BF92F3577B34DA6A3CE929D0E0E4736-00F067AA0BA902B7-01"
	trace, span, ok := parseTraceparent(upper)
	if !ok {
		t.Fatalf("大写头 %q 应可解析", upper)
	}
	want := "00-" + testTraceID + "-" + testSpanID + "-01"
	if got := formatTraceparent(trace, span, true); got != want {
		t.Errorf("再生成结果 = %q，期望 %q", got, want)
	}
}
