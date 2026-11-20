package session

import (
	"reflect"
	"strings"
	"testing"
)

// TestParseStructuredSummaryJSON 是宽容 JSON 解析的表驱动主测试，
// 覆盖合法 JSON、代码围栏、前后散文、字段别名、CJK、多候选及失败回退。
func TestParseStructuredSummaryJSON(t *testing.T) {
	canonical := `{"progress":["built lexer","added tests"],"uncertainties":["windows build fails"],"userActions":["review patch"]}`
	aliases := `{"user_intent":"build a lexer","errors_and_fixes":["undefined: token"],"next_step":"add parser tests"}`

	cases := []struct {
		name   string
		raw    string
		want   StructuredSummary
		wantOK bool
	}{
		{
			name:   "合法 JSON 规范字段",
			raw:    canonical,
			wantOK: true,
			want: StructuredSummary{
				Progress:      []string{"built lexer", "added tests"},
				Uncertainties: []string{"windows build fails"},
				UserActions:   []string{"review patch"},
			},
		},
		{
			name:   "JSON 代码围栏",
			raw:    "摘要如下：\n```json\n" + canonical + "\n```\n以上。",
			wantOK: true,
			want: StructuredSummary{
				Progress:      []string{"built lexer", "added tests"},
				Uncertainties: []string{"windows build fails"},
				UserActions:   []string{"review patch"},
			},
		},
		{
			name:   "前后散文包裹",
			raw:    "以下是本轮总结：\n" + canonical + "\n希望对你有帮助。",
			wantOK: true,
			want: StructuredSummary{
				Progress:      []string{"built lexer", "added tests"},
				Uncertainties: []string{"windows build fails"},
				UserActions:   []string{"review patch"},
			},
		},
		{
			name:   "snake_case 字段别名与单字符串值",
			raw:    aliases,
			wantOK: true,
			want: StructuredSummary{
				Progress:      []string{"build a lexer"},
				Uncertainties: []string{"undefined: token"},
				UserActions:   []string{"add parser tests"},
			},
		},
		{
			name:   "camelCase 字段别名",
			raw:    `{"userIntent":"build lexer","errorsAndFixes":["undefined: token"],"nextSteps":["add tests"]}`,
			wantOK: true,
			want: StructuredSummary{
				Progress:      []string{"build lexer"},
				Uncertainties: []string{"undefined: token"},
				UserActions:   []string{"add tests"},
			},
		},
		{
			name:   "CJK 内容原样保留",
			raw:    `{"progress":["已完成词法分析器"],"errors_and_fixes":["编译失败：未定义符号"],"next_step":["下一步：补测试"]}`,
			wantOK: true,
			want: StructuredSummary{
				Progress:      []string{"已完成词法分析器"},
				Uncertainties: []string{"编译失败：未定义符号"},
				UserActions:   []string{"下一步：补测试"},
			},
		},
		{
			name:   "CJK 字段名",
			raw:    `{"进展":["完成词法分析"],"错误":["未定义符号"],"下一步":["补测试"]}`,
			wantOK: true,
			want: StructuredSummary{
				Progress:      []string{"完成词法分析"},
				Uncertainties: []string{"未定义符号"},
				UserActions:   []string{"补测试"},
			},
		},
		{
			name:   "对象数组带文本字段",
			raw:    `{"progress":[{"text":"built lexer"},{"content":"added tests"}]}`,
			wantOK: true,
			want: StructuredSummary{
				Progress: []string{"built lexer", "added tests"},
			},
		},
		{
			name:   "嵌套包装对象",
			raw:    `{"meta":{"turns":3},"summary":{"progress":["built lexer"]}}`,
			wantOK: true,
			want: StructuredSummary{
				Progress: []string{"built lexer"},
			},
		},
		{
			name:   "首个候选非法后回退到有效候选",
			raw:    "无效片段：{not json}\n有效片段：\n```json\n" + canonical + "\n```",
			wantOK: true,
			want: StructuredSummary{
				Progress:      []string{"built lexer", "added tests"},
				Uncertainties: []string{"windows build fails"},
				UserActions:   []string{"review patch"},
			},
		},
		{
			name:   "字符串内含大括号不干扰配对",
			raw:    `{"progress":["修复 { 括号 } 解析错误"]}`,
			wantOK: true,
			want: StructuredSummary{
				Progress: []string{"修复 { 括号 } 解析错误"},
			},
		},
		{
			name:   "去重与占位条目",
			raw:    `{"progress":["done","done","(none)","无","","done again"]}`,
			wantOK: true,
			want: StructuredSummary{
				Progress: []string{"done", "done again"},
			},
		},
		{name: "非法 JSON", raw: `{not json}`, wantOK: false, want: StructuredSummary{}},
		{name: "空文本", raw: ``, wantOK: false, want: StructuredSummary{}},
		{name: "纯空白", raw: " \n\t\r\n ", wantOK: false, want: StructuredSummary{}},
		{name: "无可识别桶", raw: `{"turns":7,"title":"lexer 重构"}`, wantOK: false, want: StructuredSummary{}},
		{name: "空桶", raw: `{"progress":[],"uncertainties":[],"next_step":""}`, wantOK: false, want: StructuredSummary{}},
		{name: "纯散文", raw: "本轮只是散文总结，没有任何结构化信息。", wantOK: false, want: StructuredSummary{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseStructuredSummaryJSON(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (raw %q)", ok, tc.wantOK, tc.raw)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parsed summary mismatch:\n got %#v\nwant %#v\nraw: %s", got, tc.want, tc.raw)
			}
		})
	}
}

// TestParseStructuredSummaryJSONCJKNotEscaped 验证中文按原始 UTF-8 保留，
// 不会被转义成 \uXXXX，也不会因逐字节扫描大括号而损坏。
func TestParseStructuredSummaryJSONCJKNotEscaped(t *testing.T) {
	raw := `{"progress":["已完成用户认证"],"uncertainties":["中文{}括号测试"]}`
	got, ok := parseStructuredSummaryJSON(raw)
	if !ok {
		t.Fatalf("CJK JSON was not parsed: %s", raw)
	}
	if !reflect.DeepEqual(got.Progress, []string{"已完成用户认证"}) {
		t.Errorf("Progress = %q, want [已完成用户认证]", got.Progress)
	}
	if !reflect.DeepEqual(got.Uncertainties, []string{"中文{}括号测试"}) {
		t.Errorf("Uncertainties = %q, want [中文{}括号测试]", got.Uncertainties)
	}
	for _, s := range [][]string{got.Progress, got.Uncertainties} {
		for _, item := range s {
			if strings.Contains(item, `\u`) {
				t.Errorf("CJK item was escaped: %q", item)
			}
		}
	}
}

// TestParseStructuredSummaryLenientIsOptIn 验证宽容路径是显式 opt-in：
// JSON 输入只有宽容版本能解析，原有 ParseStructuredSummary 仍拒绝它；
// markdown 输入则两版行为一致，默认调用方不受影响。
func TestParseStructuredSummaryLenientIsOptIn(t *testing.T) {
	jsonText := `{"user_intent":"build lexer","next_step":"add tests"}`
	if _, ok := ParseStructuredSummary(jsonText); ok {
		t.Fatal("ParseStructuredSummary unexpectedly accepted JSON text")
	}
	got, ok := ParseStructuredSummaryLenient(jsonText)
	if !ok {
		t.Fatal("ParseStructuredSummaryLenient rejected JSON text")
	}
	if !reflect.DeepEqual(got.Progress, []string{"build lexer"}) {
		t.Errorf("Progress = %q, want [build lexer]", got.Progress)
	}
	if !reflect.DeepEqual(got.UserActions, []string{"add tests"}) {
		t.Errorf("UserActions = %q, want [add tests]", got.UserActions)
	}

	md := "## Progress\n- built lexer\n"
	lenient, lenientOK := ParseStructuredSummaryLenient(md)
	original, originalOK := ParseStructuredSummary(md)
	if !lenientOK || !originalOK {
		t.Fatalf("markdown parse failed: lenient=%v original=%v", lenientOK, originalOK)
	}
	if !reflect.DeepEqual(lenient, original) {
		t.Fatalf("lenient and original disagree on markdown:\n lenient %#v\noriginal %#v", lenient, original)
	}

	// 散文兜底：两版都返回 false，让调用方继续使用原始自由文本。
	prose := "只是一段散文。"
	if _, ok := ParseStructuredSummaryLenient(prose); ok {
		t.Fatal("lenient parser accepted plain prose")
	}
}
