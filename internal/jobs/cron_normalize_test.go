package jobs

import (
	"strings"
	"testing"
)

// TestNormalizeCronExprFiveFields 验证五字段表达式在最前补 "0 " 作为秒。
func TestNormalizeCronExprFiveFields(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want string
	}{
		{"全通配", "* * * * *", "0 * * * * *"},
		{"固定分钟与星期", "30 9 * * 1-5", "0 30 9 * * 1-5"},
		{"步长", "*/5 * * * *", "0 */5 * * * *"},
		{"列表与范围", "0,15,30 8-17 * * 1,3,5", "0 0,15,30 8-17 * * 1,3,5"},
		{"带步长范围", "0 0 1-31/2 1-12 0-7", "0 0 0 1-31/2 1-12 0-7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeCronExpr(tt.expr); got != tt.want {
				t.Fatalf("normalizeCronExpr(%q) = %q, want %q", tt.expr, got, tt.want)
			}
		})
	}
}

// TestNormalizeCronExprSixFields 验证六字段表达式保持字段值不变（原样）。
func TestNormalizeCronExprSixFields(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want string
	}{
		{"全通配", "0 * * * * *", "0 * * * * *"},
		{"非零秒", "15 30 9 * * 1-5", "15 30 9 * * 1-5"},
		{"秒步长", "*/10 * * * * *", "*/10 * * * * *"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeCronExpr(tt.expr); got != tt.want {
				t.Fatalf("normalizeCronExpr(%q) = %q, want %q", tt.expr, got, tt.want)
			}
		})
	}
}

// TestNormalizeCronExprInvalid 验证非法表达式返回空串。
func TestNormalizeCronExprInvalid(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"空串", ""},
		{"只有空白", "   \t  "},
		{"四字段", "* * * *"},
		{"七字段", "0 0 * * * * *"},
		{"含字母", "junk * * * *"},
		{"含 CJK", "任务 * * * *"},
		{"字段含中文", "* * 每 * 一"},
		{"含问号", "0 * ? * * *"},
		{"超长输入", "0 " + strings.Repeat("1", cronMaxExprBytes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeCronExpr(tt.expr); got != "" {
				t.Fatalf("normalizeCronExpr(%q) = %q, want empty", tt.expr, got)
			}
		})
	}
}

// TestNormalizeCronExprCJKWhitespace 验证全角空格等 CJK 空白被当作分隔符，
// 不同空白产生相同的规范输出，而字段内的 CJK 字符仍被拒绝。
func TestNormalizeCronExprCJKWhitespace(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want string
	}{
		{"全角空格分隔五字段", "*　*　*　*　*", "0 * * * * *"},
		{"制表符分隔五字段", "*\t*\t*\t*\t*", "0 * * * * *"},
		{"混合空白六字段", "0\t*　*  *\v* *", "0 * * * * *"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeCronExpr(tt.expr); got != tt.want {
				t.Fatalf("normalizeCronExpr(%q) = %q, want %q", tt.expr, got, tt.want)
			}
		})
	}

	// 字段内含 CJK 字符时，即便用全角空格分隔也必须判为非法。
	if got := normalizeCronExpr("任务　*　*　*　*"); got != "" {
		t.Fatalf("normalizeCronExpr with CJK field = %q, want empty", got)
	}
}

// TestNormalizeCronExprStable 验证同一输入多次调用结果一致（稳定），
// 且规范化结果再次规范化保持不变（幂等）。
func TestNormalizeCronExprStable(t *testing.T) {
	expr := "\t*/5  9-17　*  * 1-5\t"
	want := "0 */5 9-17 * * 1-5"
	first := normalizeCronExpr(expr)
	if first != want {
		t.Fatalf("normalizeCronExpr(%q) = %q, want %q", expr, first, want)
	}
	for i := 0; i < 5; i++ {
		if got := normalizeCronExpr(expr); got != first {
			t.Fatalf("第 %d 次调用 = %q, want %q", i+1, got, first)
		}
	}
	if twice := normalizeCronExpr(first); twice != first {
		t.Fatalf("再次规范化 = %q, want %q（幂等）", twice, first)
	}
}

// TestNormalizeCronExprBounds 验证刚好在字节上限内的合法表达式被接受，
// 超过上限即被拒绝。
func TestNormalizeCronExprBounds(t *testing.T) {
	// 构造恰好达到字节上限的六字段表达式：前五个字段 "0 1 1 1 1 " 共 10 字节，
	// 余下字节组成第六个字段，使总长正好等于上限且字段数合法。
	prefix := "0 1 1 1 1 "
	atLimit := prefix + strings.Repeat("1", cronMaxExprBytes-len(prefix))
	if got := normalizeCronExpr(atLimit); got != atLimit {
		t.Fatalf("上限内表达式被拒绝: got %q, want %q", got, atLimit)
	}
	overLimit := prefix + strings.Repeat("1", cronMaxExprBytes-len(prefix)+1)
	if got := normalizeCronExpr(overLimit); got != "" {
		t.Fatalf("超上限表达式未拒绝: got %q, want empty", got)
	}
}
