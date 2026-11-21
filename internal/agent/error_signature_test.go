package agent

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"unicode/utf8"
)

// TestNormalizeErrorSignatureVolatileParts 验证仅易变片段（UUID/时间戳/数字/路径/十六进制）
// 不同的错误被归一到同一签名，且规范化文本确实只留下占位符。
func TestNormalizeErrorSignatureVolatileParts(t *testing.T) {
	cases := []struct {
		name string
		a, b string
	}{
		{
			name: "iso8601 时间戳",
			a:    "request failed at 2026-11-21T10:00:00Z on host",
			b:    "request failed at 2026-11-22T23:59:59+08:00 on host",
		},
		{
			name: "仅日期",
			a:    "snapshot 2026-01-02 missing",
			b:    "snapshot 2027-12-31 missing",
		},
		{
			name: "数字",
			a:    "timeout after 5000ms with code 42",
			b:    "timeout after 300ms with code 7",
		},
		{
			name: "unix 绝对路径",
			a:    "failed to open /usr/local/etc/app.conf",
			b:    "failed to open /var/tmp/other/data.json",
		},
		{
			name: "windows 绝对路径",
			a:    `failed to read C:\Users\alice\project\file.go`,
			b:    `failed to read D:\work\bobby\src\other.rs`,
		},
		{
			name: "UUID",
			a:    "job 550e8400-e29b-41d4-a716-446655440000 expired",
			b:    "job 123e4567-e89b-12d3-a456-426614174000 expired",
		},
		{
			name: "十六进制串",
			a:    "bad checksum 0xDEADBEEF in frame",
			b:    "bad checksum 0x00C0FFEE in frame",
		},
		{
			name: "大小写与空白",
			a:    "  Connection   REFUSED   ",
			b:    "connection refused",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sigA, sigB := NormalizeErrorSignature(tc.a), NormalizeErrorSignature(tc.b)
			if sigA != sigB {
				t.Fatalf("签名不同：\n a=%q\n b=%q", tc.a, tc.b)
			}
			if sigA == "" {
				t.Fatal("非空错误不应得到空签名")
			}

			// 规范化文本必须真的去掉了易变内容，只保留占位符。
			textA := normalizeErrorText(tc.a)
			if stringsContainsDigits(textA) {
				t.Fatalf("规范化文本仍含数字：%q", textA)
			}
		})
	}
}

// stringsContainsDigits 报告 s 是否含有 ASCII 数字（测试辅助）。
func stringsContainsDigits(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

// TestNormalizeErrorSignatureDistinctErrors 验证真正不同的错误得到不同签名。
func TestNormalizeErrorSignatureDistinctErrors(t *testing.T) {
	distinct := []string{
		"connection refused",
		"permission denied",
		"no such file or directory",
		"invalid arguments: expected two values",
	}
	seen := make(map[string]string, len(distinct))
	for _, err := range distinct {
		sig := NormalizeErrorSignature(err)
		if prev, ok := seen[sig]; ok {
			t.Fatalf("不同错误得到相同签名：%q 与 %q -> %q", prev, err, sig)
		}
		seen[sig] = err
	}
}

// TestNormalizeErrorSignatureCJKPreserved 验证 CJK/emoji 原样保留、不被切断，
// 且含 CJK 的错误同样能按易变片段折叠、按真实内容区分。
func TestNormalizeErrorSignatureCJKPreserved(t *testing.T) {
	// CJK 与 emoji 在规范化文本中必须逐字保留。
	text := normalizeErrorText("错误 at 2026-11-21: 连接超时 🛠️ code 0XFF")
	for _, want := range []string{"错误", "连接超时", "🛠️"} {
		if !contains(text, want) {
			t.Fatalf("规范化文本 %q 未保留 %q", text, want)
		}
	}

	// 仅数字不同 -> 同一签名。
	a := NormalizeErrorSignature("连接超时：重试 3 次")
	b := NormalizeErrorSignature("连接超时：重试 9 次")
	if a != b {
		t.Fatalf("仅数字不同的 CJK 错误应归一：%q vs %q", a, b)
	}

	// 真实内容不同 -> 不同签名。
	c := NormalizeErrorSignature("权限不足：无法写入")
	if a == c {
		t.Fatalf("不同 CJK 错误不应同签名：%q", a)
	}

	// 截断必须按 rune 边界，不能产生非法 UTF-8。
	long := "错误" + repeat("字", maxSignatureRunes+50)
	sig := NormalizeErrorSignature(long)
	if !utf8.ValidString(sig) {
		t.Fatal("截断后的签名出现非法 UTF-8（切坏了多字节字符）")
	}
	if got := len([]rune(sig)); got != maxSignatureRunes {
		t.Fatalf("截断后 rune 数 = %d, want %d", got, maxSignatureRunes)
	}
}

func contains(s, sub string) bool {
	if sub == "" {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// TestErrorPatternTrackerCounts 验证 Record 返回累计次数、不同错误各自独立计数。
func TestErrorPatternTrackerCounts(t *testing.T) {
	var tracker ErrorPatternTracker
	a := "failed at 2026-11-21"
	b := "connection refused"

	if got := tracker.Record(a); got != 1 {
		t.Fatalf("第一次 Record = %d, want 1", got)
	}
	if got := tracker.Record(a); got != 2 {
		t.Fatalf("第二次 Record = %d, want 2", got)
	}
	// 仅时间戳不同 -> 同一签名，累计到 3。
	if got := tracker.Record("failed at 2027-01-02"); got != 3 {
		t.Fatalf("同签名不同时间戳 Record = %d, want 3", got)
	}
	if got := tracker.Record(b); got != 1 {
		t.Fatalf("不同错误首次 Record = %d, want 1", got)
	}
}

// TestErrorPatternTrackerTopSorting 验证 Top 按次数降序、并列时首次出现者优先、
// 上限生效，且重复调用结果稳定。
func TestErrorPatternTrackerTopSorting(t *testing.T) {
	tracker := NewErrorPatternTracker(0, 2)
	// 出现次数：z=3, a=2, m=1；上限 2 应只返回 z、a。
	for i := 0; i < 3; i++ {
		tracker.Record(" zebra error ") // 含空白，验证折叠
	}
	for i := 0; i < 2; i++ {
		tracker.Record("alpha error")
	}
	tracker.Record("middle error")

	want := []ErrorPattern{
		{Signature: "zebra error", Count: 3},
		{Signature: "alpha error", Count: 2},
	}
	got := tracker.Top()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Top() = %#v, want %#v", got, want)
	}

	// 重复调用结果稳定。
	if again := tracker.Top(); !reflect.DeepEqual(again, want) {
		t.Fatalf("Top() 结果不稳定：%#v vs %#v", again, want)
	}

	// 并列时首次出现者靠前：先记录 first 再记录 second，各 2 次。
	tie := NewErrorPatternTracker(0, 0)
	tie.Record("first")
	tie.Record("second")
	tie.Record("first")
	tie.Record("second")
	tieTop := tie.Top()
	if len(tieTop) != 2 || tieTop[0].Signature != "first" || tieTop[1].Signature != "second" {
		t.Fatalf("并列排序应按首次出现顺序：%#v", tieTop)
	}
}

// TestErrorPatternTrackerTopReturnsCopy 验证修改 Top 返回的切片不会污染内部状态。
func TestErrorPatternTrackerTopReturnsCopy(t *testing.T) {
	tracker := NewErrorPatternTracker(0, 0)
	tracker.Record("keep me")
	top := tracker.Top()
	top[0].Count = 999
	top[0].Signature = "corrupted"
	if again := tracker.Top(); again[0].Count != 1 || again[0].Signature != "keep me" {
		t.Fatalf("Top() 返回切片与内部状态别名：%#v", again)
	}
}

// TestErrorPatternTrackerWindow 验证滑动窗口淘汰最旧记录并同步扣减计数。
func TestErrorPatternTrackerWindow(t *testing.T) {
	tracker := NewErrorPatternTracker(3, 0)
	// 交替记录，窗口 3 只保留最近 3 条。
	tracker.Record("a 1") // sig a
	tracker.Record("b 1") // sig b
	tracker.Record("a 2") // sig a -> a 计数 2
	// 窗口 3 内最近三条是 b,a,a：加入第 4 条 a 时最旧的 a 被淘汰，a 计数回到 2。
	if got := tracker.Record("a 3"); got != 2 {
		t.Fatalf("窗口内 a 累计应为 2，got %d", got)
	}
	// 此时 recent = [b, a, a]，a=2, b=1。
	// 再记录一次 c 淘汰最旧的 b。
	tracker.Record("c 1")
	top := tracker.Top()
	if len(top) != 2 {
		t.Fatalf("窗口淘汰后应剩 2 个签名，got %d: %#v", len(top), top)
	}
	for _, p := range top {
		if p.Signature == "b <*>" {
			t.Fatal("最旧的 b 应被淘汰")
		}
	}
	// a 仍为 2。
	found := false
	for _, p := range top {
		if p.Signature == "a <*>" && p.Count == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("a 的计数应为 2：%#v", top)
	}
}

// TestErrorPatternTrackerDoesNotMutateInput 验证规范化与记录都不改动入参字符串。
func TestErrorPatternTrackerDoesNotMutateInput(t *testing.T) {
	input := "  Error at 2026-11-21 on /a/b/c  "
	original := input
	_ = NormalizeErrorSignature(input)
	var tracker ErrorPatternTracker
	tracker.Record(input)
	if input != original {
		t.Fatalf("入参被改动：%q -> %q", original, input)
	}
}

// TestErrorPatternTrackerConcurrent 验证并发 Record 后计数精确无损。
func TestErrorPatternTrackerConcurrent(t *testing.T) {
	tracker := NewErrorPatternTracker(0, 0)
	const workers, rounds = 32, 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				// 半数 goroutine 记录"仅数字不同"的错误（应折叠到同一签名）。
				sig := "连接错误：重试 1 次"
				if w%2 == 0 {
					sig = fmt.Sprintf("连接错误：重试 %d 次", i+2)
				}
				tracker.Record(sig)
				if w%8 == 0 {
					_ = tracker.Top() // 并发读也不应 panic
				}
			}
		}(w)
	}
	wg.Wait()

	top := tracker.Top()
	if len(top) != 1 {
		t.Fatalf("并发后应只有 1 个签名，got %d: %#v", len(top), top)
	}
	if want := int64(workers * rounds); int64(top[0].Count) != want {
		t.Fatalf("并发计数 = %d, want %d", top[0].Count, want)
	}
}

// TestErrorPatternTrackerZeroValue 验证零值追踪器可用，nil 接收方安全。
func TestErrorPatternTrackerZeroValue(t *testing.T) {
	var tracker ErrorPatternTracker
	if got := tracker.Top(); len(got) != 0 {
		t.Fatalf("零值 Top 应为空，got %#v", got)
	}
	tracker.Record("boom")
	if got := tracker.Top(); len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("零值 Record 后 Top = %#v", got)
	}
	tracker.Reset()
	if got := tracker.Top(); len(got) != 0 {
		t.Fatalf("Reset 后应为空，got %#v", got)
	}

	var nilTracker *ErrorPatternTracker
	if got := nilTracker.Record("x"); got != 0 {
		t.Fatalf("nil Record = %d, want 0", got)
	}
	if got := nilTracker.Top(); got != nil {
		t.Fatalf("nil Top = %#v, want nil", got)
	}
	nilTracker.Reset() // 不应 panic
}
