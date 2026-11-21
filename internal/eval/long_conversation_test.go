package eval

import (
	"fmt"
	"reflect"
	"testing"
	"unicode/utf8"
)

// testLongBase 是各用例共用的基础对话：一条用户、一条助手、一条工具结果。
func testLongBase() []TurnSim {
	return []TurnSim{
		{Role: "user", Content: "问题"},
		{Role: "assistant", Content: "回答"},
		{Role: "tool", Content: "结果", ToolResult: true},
	}
}

// testLongNoise 是确定性的干扰消息工厂：内容带轮次下标，奇偶决定工具结果标记。
func testLongNoise(i int) TurnSim {
	return TurnSim{Role: "noise", Content: fmt.Sprintf("干扰-%d", i), ToolResult: i%2 == 0}
}

// TestSimulateLongConversationRepeatAndNoise 表驱动覆盖不同 repeat/noise 组合，
// 逐条锁定输出顺序：每轮先重复 base，再插入该轮的 noise(i)。
func TestSimulateLongConversationRepeatAndNoise(t *testing.T) {
	base := testLongBase()
	constantNoise := func(int) TurnSim { return TurnSim{Role: "const", Content: "噪声"} }

	tests := []struct {
		name   string
		base   []TurnSim
		repeat int
		noise  func(int) TurnSim
		want   []TurnSim
	}{
		{
			name: "single round without noise", base: base, repeat: 1, noise: nil,
			want: []TurnSim{
				{Role: "user", Content: "问题"},
				{Role: "assistant", Content: "回答"},
				{Role: "tool", Content: "结果", ToolResult: true},
			},
		},
		{
			name: "single round with noise", base: base, repeat: 1, noise: testLongNoise,
			want: []TurnSim{
				{Role: "user", Content: "问题"},
				{Role: "assistant", Content: "回答"},
				{Role: "tool", Content: "结果", ToolResult: true},
				{Role: "noise", Content: "干扰-0", ToolResult: true},
			},
		},
		{
			name: "two rounds with indexed noise", base: base, repeat: 2, noise: testLongNoise,
			want: []TurnSim{
				{Role: "user", Content: "问题"},
				{Role: "assistant", Content: "回答"},
				{Role: "tool", Content: "结果", ToolResult: true},
				{Role: "noise", Content: "干扰-0", ToolResult: true},
				{Role: "user", Content: "问题"},
				{Role: "assistant", Content: "回答"},
				{Role: "tool", Content: "结果", ToolResult: true},
				{Role: "noise", Content: "干扰-1", ToolResult: false},
			},
		},
		{
			name: "three rounds with constant noise", base: base, repeat: 3, noise: constantNoise,
			want: []TurnSim{
				{Role: "user", Content: "问题"}, {Role: "assistant", Content: "回答"},
				{Role: "tool", Content: "结果", ToolResult: true}, {Role: "const", Content: "噪声"},
				{Role: "user", Content: "问题"}, {Role: "assistant", Content: "回答"},
				{Role: "tool", Content: "结果", ToolResult: true}, {Role: "const", Content: "噪声"},
				{Role: "user", Content: "问题"}, {Role: "assistant", Content: "回答"},
				{Role: "tool", Content: "结果", ToolResult: true}, {Role: "const", Content: "噪声"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SimulateLongConversation(tt.base, tt.repeat, tt.noise)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SimulateLongConversation() =\n got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

// TestSimulateLongConversationCJK 覆盖中文与 emoji：内容逐字节保留、UTF-8 始终有效，
// 干扰消息中的 CJK 也被原样插入，且不会被截断成半个 rune。
func TestSimulateLongConversationCJK(t *testing.T) {
	base := []TurnSim{
		{Role: "用户", Content: "请分析这段代码🙂"},
		{Role: "工具", Content: "文件「你好.go」已读取", ToolResult: true},
	}
	noise := func(i int) TurnSim {
		return TurnSim{Role: "噪声", Content: fmt.Sprintf("第%d轮干扰🚀", i)}
	}

	got := SimulateLongConversation(base, 2, noise)
	if len(got) != 6 {
		t.Fatalf("len = %d, want 6 (2 rounds x (2 base + 1 noise))", len(got))
	}
	// CJK 与 emoji 的 rune 数小于字节数，确保测试确实覆盖多字节字符。
	const sample = "请分析这段代码🙂"
	if utf8.RuneCountInString(sample) >= len(sample) {
		t.Fatalf("test premise broken: sample is ASCII-sized")
	}
	for i, turn := range got {
		if !utf8.ValidString(turn.Content) {
			t.Fatalf("turn %d content is invalid UTF-8: %q", i, turn.Content)
		}
	}
	if want := base[0].Content; got[0].Content != want {
		t.Fatalf("got[0].Content = %q, want %q", got[0].Content, want)
	}
	if want := "第0轮干扰🚀"; got[2].Content != want {
		t.Fatalf("got[2].Content = %q, want %q", got[2].Content, want)
	}
	if want := "第1轮干扰🚀"; got[5].Content != want {
		t.Fatalf("got[5].Content = %q, want %q", got[5].Content, want)
	}
}

// TestSimulateLongConversationDoesNotMutateBase 锁定纯函数语义：
// 调用前后 base 完全相等，且改动输出不会写回 base（无共享底层数组）。
func TestSimulateLongConversationDoesNotMutateBase(t *testing.T) {
	base := testLongBase()
	before := append([]TurnSim(nil), base...)

	out := SimulateLongConversation(base, 3, testLongNoise)
	if !reflect.DeepEqual(base, before) {
		t.Fatalf("base mutated:\n got %+v\nwant %+v", base, before)
	}

	// 修改返回的切片不应影响 base：元素是按值复制的结构体。
	out[0].Content = "被改写"
	out[0].Role = "被改写"
	if !reflect.DeepEqual(base, before) {
		t.Fatalf("output aliases base; base changed after editing output:\n got %+v\nwant %+v", base, before)
	}
}

// TestSimulateLongConversationDeterministic 证明相同入参与确定性 noise 重复调用得到相同结果，
// 不同 noise 产生不同结果（输出确实由 noise 注入决定）。
func TestSimulateLongConversationDeterministic(t *testing.T) {
	base := testLongBase()
	first := SimulateLongConversation(base, 4, testLongNoise)
	second := SimulateLongConversation(base, 4, testLongNoise)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("not deterministic:\nfirst  %+v\nsecond %+v", first, second)
	}

	otherNoise := func(i int) TurnSim { return TurnSim{Role: "other", Content: fmt.Sprintf("其他-%d", i)} }
	other := SimulateLongConversation(base, 4, otherNoise)
	if reflect.DeepEqual(first, other) {
		t.Fatalf("different noise produced identical output: %+v", first)
	}
}

// TestSimulateLongConversationEmptyBase 覆盖 base 为 nil/空切片：
// 仍按轮次插入 noise；noise 为 nil 时返回空切片。
func TestSimulateLongConversationEmptyBase(t *testing.T) {
	for _, tc := range []struct {
		name   string
		base   []TurnSim
		repeat int
		noise  func(int) TurnSim
		want   []TurnSim
	}{
		{
			name: "nil base with noise", base: nil, repeat: 2, noise: testLongNoise,
			want: []TurnSim{
				{Role: "noise", Content: "干扰-0", ToolResult: true},
				{Role: "noise", Content: "干扰-1", ToolResult: false},
			},
		},
		{
			name: "empty base without noise", base: []TurnSim{}, repeat: 5, noise: nil,
			want: []TurnSim{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SimulateLongConversation(tc.base, tc.repeat, tc.noise)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestSimulateLongConversationInvalidRepeatFallback 覆盖上限保护/非法值兜底：
// repeat 为 0、负数时回退为 defaultRepeat 单轮，而不是空输出或 panic。
func TestSimulateLongConversationInvalidRepeatFallback(t *testing.T) {
	base := testLongBase()
	for _, repeat := range []int{0, -1, -100} {
		t.Run(fmt.Sprintf("repeat=%d", repeat), func(t *testing.T) {
			got := SimulateLongConversation(base, repeat, testLongNoise)
			want := SimulateLongConversation(base, defaultRepeat, testLongNoise)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("repeat=%d got %+v, want single round %+v", repeat, got, want)
			}
			if len(got) != len(base)+1 {
				t.Fatalf("repeat=%d len = %d, want %d", repeat, len(got), len(base)+1)
			}
		})
	}
}

// TestConversationStats 表驱动锁定统计的正确性：total、byRole、toolResults、uniqueContent。
func TestConversationStats(t *testing.T) {
	tests := []struct {
		name       string
		turns      []TurnSim
		wantTotal  int
		wantByRole map[string]int
		wantTool   int
		wantUnique int
	}{
		{
			name: "mixed roles duplicates and tool results",
			turns: []TurnSim{
				{Role: "user", Content: "a", ToolResult: false},
				{Role: "assistant", Content: "b", ToolResult: false},
				{Role: "tool", Content: "c", ToolResult: true},
				{Role: "user", Content: "a", ToolResult: false},
				{Role: "tool", Content: "d", ToolResult: true},
			},
			wantTotal:  5,
			wantByRole: map[string]int{"user": 2, "assistant": 1, "tool": 2},
			wantTool:   2,
			wantUnique: 4, // a、b、c、d
		},
		{
			name: "empty role and empty content count as categories",
			turns: []TurnSim{
				{Content: ""},
				{Content: ""},
				{Role: "user", Content: "x"},
			},
			wantTotal:  3,
			wantByRole: map[string]int{"": 2, "user": 1},
			wantTool:   0,
			wantUnique: 2, // 空串与 "x"
		},
		{
			name: "tool result flag independent of role",
			turns: []TurnSim{
				{Role: "user", Content: "x", ToolResult: true},
				{Role: "tool", Content: "y", ToolResult: false},
			},
			wantTotal:  2,
			wantByRole: map[string]int{"user": 1, "tool": 1},
			wantTool:   1,
			wantUnique: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, byRole, toolResults, uniqueContent := ConversationStats(tt.turns)
			if total != tt.wantTotal {
				t.Errorf("total = %d, want %d", total, tt.wantTotal)
			}
			if !reflect.DeepEqual(byRole, tt.wantByRole) {
				t.Errorf("byRole = %v, want %v", byRole, tt.wantByRole)
			}
			if toolResults != tt.wantTool {
				t.Errorf("toolResults = %d, want %d", toolResults, tt.wantTool)
			}
			if uniqueContent != tt.wantUnique {
				t.Errorf("uniqueContent = %d, want %d", uniqueContent, tt.wantUnique)
			}
		})
	}
}

// TestConversationStatsCJK 覆盖中文/emoji：内容按字节精确去重，
// 形近但不同的文本不被合并，重复的多字节文本只计一次。
func TestConversationStatsCJK(t *testing.T) {
	turns := []TurnSim{
		{Role: "用户", Content: "你好🙂"},
		{Role: "用户", Content: "你好🙂"},                   // 与上一条完全相同，只计一次
		{Role: "工具", Content: "你好🙃", ToolResult: true}, // emoji 不同，应单独计数
		{Role: "工具", Content: "世界", ToolResult: true},
	}
	total, byRole, toolResults, uniqueContent := ConversationStats(turns)
	if total != 4 {
		t.Errorf("total = %d, want 4", total)
	}
	wantByRole := map[string]int{"用户": 2, "工具": 2}
	if !reflect.DeepEqual(byRole, wantByRole) {
		t.Errorf("byRole = %v, want %v", byRole, wantByRole)
	}
	if toolResults != 2 {
		t.Errorf("toolResults = %d, want 2", toolResults)
	}
	if uniqueContent != 3 {
		t.Errorf("uniqueContent = %d, want 3 (你好🙂 / 你好🙃 / 世界)", uniqueContent)
	}
}

// TestConversationStatsEmptyInput 锁定空输入：各计数为 0，byRole 为非 nil 空 map。
func TestConversationStatsEmptyInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		turns []TurnSim
	}{
		{"nil slice", nil},
		{"empty slice", []TurnSim{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total, byRole, toolResults, uniqueContent := ConversationStats(tc.turns)
			if total != 0 || toolResults != 0 || uniqueContent != 0 {
				t.Fatalf("got total=%d tool=%d unique=%d, want all 0", total, toolResults, uniqueContent)
			}
			if byRole == nil {
				t.Fatalf("byRole is nil, want non-nil empty map")
			}
			if len(byRole) != 0 {
				t.Fatalf("byRole = %v, want empty", byRole)
			}
		})
	}
}

// TestConversationStatsDoesNotMutateInput 锁定 ConversationStats 的纯函数语义。
func TestConversationStatsDoesNotMutateInput(t *testing.T) {
	turns := []TurnSim{
		{Role: "user", Content: "问题"},
		{Role: "tool", Content: "结果", ToolResult: true},
	}
	before := append([]TurnSim(nil), turns...)
	ConversationStats(turns)
	if !reflect.DeepEqual(turns, before) {
		t.Fatalf("ConversationStats mutated input:\n got %+v\nwant %+v", turns, before)
	}
}

// TestSimulateThenStatsRoundTrip 把两个原语串起来：模拟长对话后统计，
// 断言 total/byRole/toolResults 与按构造规则推算出的期望一致。
func TestSimulateThenStatsRoundTrip(t *testing.T) {
	base := testLongBase() // user / assistant / tool(工具结果)
	repeat := 4
	turns := SimulateLongConversation(base, repeat, testLongNoise)

	total, byRole, toolResults, uniqueContent := ConversationStats(turns)
	if total != repeat*(len(base)+1) {
		t.Errorf("total = %d, want %d", total, repeat*(len(base)+1))
	}
	// 每轮贡献：user/assistant/tool/noise 各一条。
	wantByRole := map[string]int{
		"user": repeat, "assistant": repeat, "tool": repeat, "noise": repeat,
	}
	if !reflect.DeepEqual(byRole, wantByRole) {
		t.Errorf("byRole = %v, want %v", byRole, wantByRole)
	}
	// tool 标记：base 每轮 1 条 + noise 中偶数轮（0、2）各 1 条 = 4 + 2 = 6。
	if toolResults != repeat+2 {
		t.Errorf("toolResults = %d, want %d", toolResults, repeat+2)
	}
	// 去重内容：base 三条固定 + noise 四条（干扰-0..3）= 7。
	if uniqueContent != len(base)+repeat {
		t.Errorf("uniqueContent = %d, want %d", uniqueContent, len(base)+repeat)
	}
}
