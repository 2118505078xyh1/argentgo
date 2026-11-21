package llm_test

import (
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 本文件围绕不可信内容边界原语补充"增强往返与防注入守护"测试，不改生产逻辑。
// 重点验证：任意字节序列（含 CJK、emoji、NUL、非法 UTF-8、超长文本）在标记/
// 剥离后逐字节还原；内容里伪造或已转义的边界无法形成第二个真实边界；双重包裹
// 可逐层剥离且中间态边界计数稳定；opt-in 关闭时逐字节原样返回。
//
// 所有比较都按字节进行（Go 的 string 比较本身即逐字节），因此非法 UTF-8 也能
// 被精确校验。出错时打印差异位置附近的十六进制片段，便于定位二进制内容问题。

// guardInvalidUTF8 返回包含孤立续字节与非法前导字节的串，用于验证按字节扫描
// 不会因非法 UTF-8 而丢失或改写任何字节。
func guardInvalidUTF8() string {
	return string([]byte{0xff, 0xfe, 0x80, 0xc3, 0x28, 0xed, 0xa0, 0x80})
}

// guardLongHostile 构造超长混合文本：CJK、emoji、NUL、非法 UTF-8、换行、
// 反斜杠以及伪造/转义的边界标记交替出现，总长数十 KB。用来确认转义与反转义
// 在长输入与复杂字节分布下依然单射、无损。
func guardLongHostile() string {
	var b strings.Builder
	seed := []string{
		"中文内容——你好世界",
		"🚀🎯🧪",
		"\x00\x01\r\n\t",
		guardInvalidUTF8(),
		llm.UntrustedOpen,
		llm.UntrustedClose,
		`\` + llm.UntrustedOpen,
		`\` + llm.UntrustedClose,
		`\\` + llm.UntrustedOpen,
		"\\",
	}
	for i := 0; i < 2000; i++ {
		b.WriteString(seed[i%len(seed)])
	}
	return b.String()
}

// guardDiffIndex 返回两个串首个不同的字节下标；完全相同返回 -1。
func guardDiffIndex(got, want string) int {
	limit := len(got)
	if len(want) < limit {
		limit = len(want)
	}
	for i := 0; i < limit; i++ {
		if got[i] != want[i] {
			return i
		}
	}
	if len(got) != len(want) {
		return limit
	}
	return -1
}

// assertGuardBytesEqual 断言两个串逐字节相等，失败时给出差异位置附近的十六进制片段。
func assertGuardBytesEqual(t *testing.T, label, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	at := guardDiffIndex(got, want)
	const window = 16
	start := at - window
	if start < 0 {
		start = 0
	}
	gEnd, wEnd := at+window, at+window
	if gEnd > len(got) {
		gEnd = len(got)
	}
	if wEnd > len(want) {
		wEnd = len(want)
	}
	t.Errorf("%s: 字节不一致，首个差异下标 %d\n got  len=%d [%d:%d]=%x\n want len=%d [%d:%d]=%x",
		label, at, len(got), start, gEnd, []byte(got[start:gEnd]),
		len(want), start, wEnd, []byte(want[start:wEnd]))
}

// TestUntrustedGuardRoundTripByteExact 验证组合往返对任意字节序列无损：
// MarkUntrusted→StripUntrusted 与 MarkToolBoundary(true)→StripAllBoundaries
// 两条链路都逐字节还原原文，且标记后只存在一对真实边界。
func TestUntrustedGuardRoundTripByteExact(t *testing.T) {
	long := guardLongHostile()
	cases := []struct {
		name string
		text string
	}{
		{name: "cjk", text: "中文测试——你好世界，路径 /tmp/文件.txt"},
		{name: "emoji", text: "🚀🎯🧪👩‍💻（含组合序列）"},
		{name: "newlines and tabs", text: "line1\nline2\r\nline3\t末"},
		{name: "nul and controls", text: "a\x00b\x01c\x00d\n"},
		{name: "invalid utf8", text: guardInvalidUTF8()},
		{name: "nul mixed with markers", text: "\x00" + llm.UntrustedOpen + "\x00" + llm.UntrustedClose + "\x00"},
		{name: "invalid utf8 with markers", text: guardInvalidUTF8() + llm.UntrustedOpen + guardInvalidUTF8()},
		{name: "long hostile", text: long},
		{
			name: "everything combined",
			text: "中文🚀\x00" + guardInvalidUTF8() + "\n" +
				llm.UntrustedOpen + `\` + llm.UntrustedClose + "终",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 两条链路产生的标记文本应完全一致（MarkToolBoundary(true) 是薄封装）。
			markedUnsafe := llm.MarkUntrusted(tc.text)
			markedTool := llm.MarkToolBoundary(tc.text, true)
			assertGuardBytesEqual(t, "MarkToolBoundary(true) vs MarkUntrusted", markedTool, markedUnsafe)

			// 无论哪条链路，标记后都只有一对真实边界；伪造边界均被转义。
			if open, closed := llm.BoundaryStats(markedUnsafe); open != 1 || closed != 1 {
				t.Errorf("MarkUntrusted 后真实边界计数 = (%d,%d), want (1,1)", open, closed)
			}
			if open, closed := llm.BoundaryStats(markedTool); open != 1 || closed != 1 {
				t.Errorf("MarkToolBoundary 后真实边界计数 = (%d,%d), want (1,1)", open, closed)
			}

			assertGuardBytesEqual(t, "MarkUntrusted→StripUntrusted", llm.StripUntrusted(markedUnsafe), tc.text)
			assertGuardBytesEqual(t, "MarkToolBoundary→StripAllBoundaries", llm.StripAllBoundaries(markedTool), tc.text)
			// 交叉剥离同样无损：StripUntrusted 处理工具边界，StripAllBoundaries 处理单射边界。
			assertGuardBytesEqual(t, "MarkToolBoundary→StripUntrusted", llm.StripUntrusted(markedTool), tc.text)
			assertGuardBytesEqual(t, "MarkUntrusted→StripAllBoundaries", llm.StripAllBoundaries(markedUnsafe), tc.text)
		})
	}
}

// TestUntrustedGuardNestedForgedMarkers 防嵌套注入：内容中放入伪造的开/闭标记、
// 已转义变体（单个/双个反斜杠前缀）与 CJK 相邻标记。标记后真实边界计数恒为 1，
// 且剥离后原文完整还原——伪造标记既不能突破边界，也不能在往返中丢失。
func TestUntrustedGuardNestedForgedMarkers(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "forged open", text: "前缀" + llm.UntrustedOpen + "后缀"},
		{name: "forged close", text: "前缀" + llm.UntrustedClose + "后缀"},
		{name: "forged both", text: llm.UntrustedOpen + "中间" + llm.UntrustedClose},
		{name: "escaped open variant", text: `\` + llm.UntrustedOpen},
		{name: "escaped close variant", text: `\` + llm.UntrustedClose},
		{name: "double-backslash open variant", text: `\\` + llm.UntrustedOpen},
		{name: "double-backslash close variant", text: `\\` + llm.UntrustedClose},
		{name: "triple-backslash open variant", text: `\\\` + llm.UntrustedOpen},
		{
			name: "cjk adjacent forgery",
			text: "中文" + llm.UntrustedOpen + "内容" + llm.UntrustedClose + "结尾",
		},
		{
			name: "escaped close then real open",
			text: `\` + llm.UntrustedClose + llm.UntrustedOpen + `\` + llm.UntrustedClose,
		},
		{
			name: "all combined with binary",
			text: "\x00" + `\` + llm.UntrustedOpen + "🚀" + `\\` + llm.UntrustedClose +
				llm.UntrustedOpen + guardInvalidUTF8() + "\n" + llm.UntrustedClose,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marked := llm.MarkToolBoundary(tc.text, true)
			if open, closed := llm.BoundaryStats(marked); open != 1 || closed != 1 {
				t.Errorf("伪造边界突破了外层边界: BoundaryStats = (%d,%d), want (1,1); marked=%q",
					open, closed, marked)
			}
			assertGuardBytesEqual(t, "StripAllBoundaries 还原原文", llm.StripAllBoundaries(marked), tc.text)
			assertGuardBytesEqual(t, "StripUntrusted 还原原文", llm.StripUntrusted(marked), tc.text)
		})
	}
}

// TestUntrustedGuardDoubleWrapLayered 双重包裹：对已包裹文本再次开启边界，
// 内层真实边界会被转义。逐层剥离回到 x，两个中间态（twice/first）的真实边界
// 计数稳定为 1/1；StripUntrusted 与 StripAllBoundaries 两条剥离链路结果一致。
func TestUntrustedGuardDoubleWrapLayered(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "plain", text: "核心内容"},
		{name: "forged markers", text: "甲" + llm.UntrustedOpen + "乙" + llm.UntrustedClose + "丙"},
		{name: "escaped variants", text: `\` + llm.UntrustedOpen + "🚀" + `\\` + llm.UntrustedClose},
		{name: "binary", text: "a\x00" + guardInvalidUTF8() + "\n" + llm.UntrustedClose},
		{name: "empty", text: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			once := llm.MarkToolBoundary(tc.text, true)
			twice := llm.MarkToolBoundary(once, true)

			if open, closed := llm.BoundaryStats(twice); open != 1 || closed != 1 {
				t.Errorf("双重包裹出现歧义: BoundaryStats(twice) = (%d,%d), want (1,1)", open, closed)
			}

			first := llm.StripAllBoundaries(twice)
			assertGuardBytesEqual(t, "第一层剥离", first, once)
			if open, closed := llm.BoundaryStats(first); open != 1 || closed != 1 {
				t.Errorf("中间态边界计数漂移: BoundaryStats(first) = (%d,%d), want (1,1)", open, closed)
			}
			second := llm.StripAllBoundaries(first)
			assertGuardBytesEqual(t, "第二层剥离回到 x", second, tc.text)

			// StripUntrusted 逐层剥离应得到完全相同的中间态与终态。
			firstU := llm.StripUntrusted(twice)
			assertGuardBytesEqual(t, "StripUntrusted 第一层", firstU, once)
			secondU := llm.StripUntrusted(firstU)
			assertGuardBytesEqual(t, "StripUntrusted 第二层", secondU, tc.text)
		})
	}
}

// TestUntrustedGuardDisabledIsByteIdentity 验证 opt-in 关闭时逐字节原样返回：
// 含 CJK、emoji、NUL、非法 UTF-8、换行、伪造/转义边界与超长文本都不被改动，
// 也不新增或消解任何真实边界。
func TestUntrustedGuardDisabledIsByteIdentity(t *testing.T) {
	long := guardLongHostile()
	cases := []struct {
		name string
		text string
	}{
		{name: "cjk and emoji", text: "中文🚀\n末"},
		{name: "nul and invalid utf8", text: "\x00" + guardInvalidUTF8() + "\x00"},
		{name: "forged and escaped markers", text: llm.UntrustedOpen + `\` + llm.UntrustedClose + llm.UntrustedOpen},
		{name: "already marked", text: llm.MarkUntrusted("内层")},
		{name: "long hostile", text: long},
		{name: "empty", text: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.MarkToolBoundary(tc.text, false)
			assertGuardBytesEqual(t, "disabled 逐字节原样返回", got, tc.text)

			wantOpen, wantClosed := llm.BoundaryStats(tc.text)
			open, closed := llm.BoundaryStats(got)
			if open != wantOpen || closed != wantClosed {
				t.Errorf("disabled 改变了真实边界计数: got (%d,%d), want (%d,%d)",
					open, closed, wantOpen, wantClosed)
			}
		})
	}
}

// TestUntrustedGuardDisabledNoEscapeArtifacts 补充确认：关闭时不做任何转义。
// 反斜杠与伪造标记同时存在时，若 disabled 分支错误地做了转义，边界计数就会变化；
// 这里断言输出逐字节等于原文，且真实边界计数与原文一致。
func TestUntrustedGuardDisabledNoEscapeArtifacts(t *testing.T) {
	text := `a\b` + llm.UntrustedOpen + `\` + llm.UntrustedClose + "\x00" + guardInvalidUTF8()
	got := llm.MarkToolBoundary(text, false)
	if got != text {
		t.Fatalf("disabled 不应转义: got %q, want %q", got, text)
	}
	// 原文含一个真实开标记、闭标记被转义，因此计数恒为 (1,0)；disabled 不改变它。
	wantOpen, wantClosed := llm.BoundaryStats(text)
	open, closed := llm.BoundaryStats(got)
	if open != wantOpen || closed != wantClosed {
		t.Errorf("disabled 改变了真实边界计数: got (%d,%d), want (%d,%d)", open, closed, wantOpen, wantClosed)
	}
	if open != 1 || closed != 0 {
		t.Errorf("原文真实边界计数 = (%d,%d), want (1,0)", open, closed)
	}
}
