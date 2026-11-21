package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// pathEntry 构建工具条目，其载荷携带文件路径，匹配
// pathsFromData 收集的 "path" 键。
func pathEntry(path string) Entry {
	data, _ := json.Marshal(map[string]any{"path": path})
	return Entry{Role: "tool", Name: "edit", Data: data}
}

func TestSummarizeKeepsNextStepsOnNonFirstLine(t *testing.T) {
	dropped := []Entry{
		{Role: "assistant", Content: "已改完 X。\n\nNext steps: 加 lexer 并补测试"},
	}
	got := Summarize(dropped, 0)
	if len(got.NextSteps) != 1 {
		t.Fatalf("NextSteps = %q, want exactly one entry", got.NextSteps)
	}
	if !strings.Contains(got.NextSteps[0], "加 lexer 并补测试") {
		t.Fatalf("NextSteps = %q, want the real next-step line", got.NextSteps)
	}
	if strings.Contains(got.NextSteps[0], "已改完") {
		t.Fatalf("next step was taken from the first line: %q", got.NextSteps)
	}
	if !strings.Contains(got.Summary, "加 lexer 并补测试") {
		t.Fatalf("summary dropped next steps:\n%s", got.Summary)
	}
}

func TestSummarizeKeepsFailureOnNonFirstLine(t *testing.T) {
	dropped := []Entry{
		{Role: "tool", Name: "shell", Content: "compiling...\nbuilding\nerror: undefined: token\nat 42"},
	}
	got := Summarize(dropped, 0)
	if len(got.Failures) != 1 {
		t.Fatalf("Failures = %q, want exactly one entry", got.Failures)
	}
	if !strings.Contains(got.Failures[0], "error: undefined: token") {
		t.Fatalf("Failures = %q, want the error line", got.Failures)
	}
	if strings.Contains(got.Failures[0], "compiling") {
		t.Fatalf("failure was taken from the first line: %q", got.Failures)
	}
	if !strings.Contains(got.Summary, "error: undefined: token") {
		t.Fatalf("summary dropped the failure:\n%s", got.Summary)
	}
}

func TestSummarizeKeepsMostRecentFilesByAppearanceOrder(t *testing.T) {
	// 出现顺序：字典序最大的三个路径最早，
	// 字典序最小的三个最新。在 tailItems 之前排序（旧
	// 行为）会保留错误的十二条。
	paths := []string{
		"z_old1.go", "z_old2.go", "z_old3.go",
		"m04.go", "m05.go", "m06.go", "m07.go", "m08.go",
		"m09.go", "m10.go", "m11.go", "m12.go",
		"a_new1.go", "a_new2.go", "a_new3.go",
	}
	var dropped []Entry
	for _, path := range paths {
		dropped = append(dropped, pathEntry(path))
	}
	got := Summarize(dropped, 0)
	if len(got.Files) != 12 {
		t.Fatalf("Files = %q, want 12 entries", got.Files)
	}
	seen := make(map[string]bool)
	for _, file := range got.Files {
		seen[file] = true
	}
	for _, want := range []string{"a_new1.go", "a_new2.go", "a_new3.go"} {
		if !seen[want] {
			t.Errorf("most recent file %q missing from Files: %q", want, got.Files)
		}
	}
	for _, stale := range []string{"z_old1.go", "z_old2.go", "z_old3.go"} {
		if seen[stale] {
			t.Errorf("oldest file %q should have been dropped, got Files: %q", stale, got.Files)
		}
	}
	// 显示顺序仍然排序，以保持摘要稳定。
	if got.Files[0] != "a_new1.go" || got.Files[11] != "m12.go" {
		t.Errorf("Files not sorted for display: %q", got.Files)
	}
}

// Summarize 在排序前只保留最近十二条文件路径。当
// 十二条路径的截断点恰好落在携带多条路径的单个载荷内部时，
// 所选择的子集不得依赖 Go 随机化的 map 迭代：
// 同一份转录必须始终产生相同的 "Files changed" 章节。
// 这镜像了 search.go 中 collectStrings 使用的确定性键规则。
func TestSummarizeFilesAreDeterministicAcrossRuns(t *testing.T) {
	var dropped []Entry
	for i := 0; i < 5; i++ {
		dropped = append(dropped, pathEntry(fmt.Sprintf("a%d.go", i)))
	}
	// 一个以随机 map 顺序携带十条路径的载荷；与下面的条目组合后，
	// tailItems(files, 12) 恰好切过这个块。
	multi, err := json.Marshal(map[string]any{
		"path":      "p1.go",
		"file":      "p2.go",
		"files":     []string{"p3.go", "p4.go", "p5.go", "p6.go"},
		"arguments": `{"path":"p7.go","file":"p8.go","files":["p9.go","p10.go"]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	dropped = append(dropped, Entry{Role: "tool", Name: "edit", Data: multi})
	for i := 0; i < 8; i++ {
		dropped = append(dropped, pathEntry(fmt.Sprintf("b%d.go", i)))
	}

	want := ""
	for _, file := range Summarize(dropped, 0).Files {
		want += file + "|"
	}
	for n := 0; n < 300; n++ {
		got := ""
		for _, file := range Summarize(dropped, 0).Files {
			got += file + "|"
		}
		if got != want {
			t.Fatalf("Summarize.Files changed across runs:\n got %s\nwant %s", got, want)
		}
	}
	// 无论内部出现顺序如何，最早的五条路径必须
	// 被丢弃，最新的八条必须被保留。
	files := Summarize(dropped, 0).Files
	if len(files) != 12 {
		t.Fatalf("Files = %q, want 12 entries", files)
	}
	kept := make(map[string]bool, len(files))
	for _, file := range files {
		kept[file] = true
	}
	for i := 0; i < 5; i++ {
		if kept[fmt.Sprintf("a%d.go", i)] {
			t.Errorf("oldest file a%d.go survived the twelve-path cut: %q", i, files)
		}
	}
	for i := 0; i < 8; i++ {
		if !kept[fmt.Sprintf("b%d.go", i)] {
			t.Errorf("newest file b%d.go missing from Files: %q", i, files)
		}
	}
}

// TestSummarizeDoesNotPromoteSummaryHeadersToNextSteps 覆盖重复压缩：
// 压缩记录被标记为 RetainAlways，因此后续
// 压缩会把先前的摘要视为 system Entry（其 Data 是旧的
// Compaction 载荷），并对其 markdown 文本重新运行下一步启发式。
// 旧摘要自身的 "## Next steps" 标题匹配 "next step"
// 标记，过去常在新摘要中变成字面量 "- ## Next steps" 的项目符号。
// markdown 标题是结构，绝不是计划。
func TestSummarizeDoesNotPromoteSummaryHeadersToNextSteps(t *testing.T) {
	oldSummary := "## Compaction\n- dropped 2 message(s), kept the last 1\n\n" +
		"## Goal\n- build lexer\n\n" +
		"## Files changed\n- (none)\n\n" +
		"## Failures\n- (none)\n\n" +
		"## Next steps\n- (none)\n"
	data, _ := json.Marshal(map[string]any{"summary": oldSummary})
	got := Summarize([]Entry{{Role: "system", Content: oldSummary, Data: data}}, 0)
	for _, step := range got.NextSteps {
		if strings.HasPrefix(strings.TrimSpace(step), "#") {
			t.Fatalf("markdown header promoted to next step: %q\nsummary:\n%s", step, got.Summary)
		}
	}
	if strings.Contains(got.Summary, "- ## ") {
		t.Fatalf("summary contains a promoted section header:\n%s", got.Summary)
	}
}

// TestSummarizeStillKeepsGenuineNextStepAfterHeader 防止上面的修复
// 丢弃同一内容中跟在标题之后的真实下一步。
func TestSummarizeStillKeepsGenuineNextStepAfterHeader(t *testing.T) {
	content := "## Plan\nNext steps: 加 lexer 并补测试"
	data, _ := json.Marshal(map[string]any{"summary": content})
	got := Summarize([]Entry{{Role: "system", Content: content, Data: data}}, 0)
	if len(got.NextSteps) != 1 || !strings.Contains(got.NextSteps[0], "加 lexer 并补测试") {
		t.Fatalf("genuine next step after a heading was lost: %q", got.NextSteps)
	}
}

func TestSummarizeKeepsFirstLineMarkers(t *testing.T) {
	dropped := []Entry{
		{Role: "assistant", Content: "Next steps: 加 lexer 并补测试"},
		{Role: "tool", Name: "compile", Content: "error: build failed"},
	}
	got := Summarize(dropped, 0)
	if len(got.NextSteps) != 1 || !strings.Contains(got.NextSteps[0], "加 lexer") {
		t.Fatalf("first-line next step regressed: %q", got.NextSteps)
	}
	if len(got.Failures) != 1 || !strings.Contains(got.Failures[0], "error: build failed") {
		t.Fatalf("first-line failure regressed: %q", got.Failures)
	}
}

func TestSummarizeUTF8ChineseAndClipping(t *testing.T) {
	longStep := "下一步: " + strings.Repeat("补中文注释并跑测试", 60) // >200 符文
	dropped := []Entry{
		{Role: "user", Content: "目标：实现词法分析器"},
		{Role: "assistant", Content: "已完成解析器。\n" + longStep},
		{Role: "tool", Name: "编译", Content: "编译中...\n失败: 未定义的符号 token\n位置 42"},
	}
	got := Summarize(dropped, 0)

	if len(got.NextSteps) != 1 {
		t.Fatalf("NextSteps = %q, want exactly one entry", got.NextSteps)
	}
	step := got.NextSteps[0]
	if !strings.HasPrefix(step, "下一步:") {
		t.Fatalf("Chinese next-step marker lost: %q", step)
	}
	if !utf8.ValidString(step) {
		t.Fatalf("clipped next step is not valid UTF-8: %q", step)
	}
	if r := utf8.RuneCountInString(step); r > 200 {
		t.Fatalf("clipped next step has %d runes, want <= 200", r)
	}

	if len(got.Failures) != 1 || !strings.Contains(got.Failures[0], "失败: 未定义的符号 token") {
		t.Fatalf("Chinese failure on non-first line lost: %q", got.Failures)
	}
	if !strings.Contains(got.Summary, "目标：实现词法分析器") {
		t.Fatalf("UTF-8 goal lost in summary:\n%s", got.Summary)
	}
}

// TestSummarizeKeepsOriginalGoalAfterSyntheticUserFlood 是 R28 长会话审计的守卫：
// steering、预算提醒、纠错引导都以 Role:"user" 追加。当这些合成轮超过五条时，
// 旧实现 goals=tailItems(goals,5) 会把窗口内最早的原始目标挤出 Goal 段。
func TestSummarizeKeepsOriginalGoalAfterSyntheticUserFlood(t *testing.T) {
	originalGoal := "实现并发布词法分析器（原始目标）"
	dropped := []Entry{{Role: "user", Content: originalGoal}}
	synthetic := []string{
		"[steering] 请改用表驱动测试",
		"[budget reminder] 上下文预算即将用尽",
		"[mistake guidance] 上一次提交遗漏了 gofmt",
		"[steering] 优先修复解析错误",
		"[budget reminder] 请尽快收尾",
		"[mistake guidance] 不要修改 vendor 目录",
	}
	for _, text := range synthetic {
		dropped = append(dropped, Entry{Role: "user", Content: text})
	}

	got := Summarize(dropped, 0)
	goalStart := strings.Index(got.Summary, "## Goal\n")
	filesStart := strings.Index(got.Summary, "## Files changed\n")
	goalPos := strings.Index(got.Summary, originalGoal)
	if goalStart < 0 || filesStart < 0 {
		t.Fatalf("summary is missing Goal/Files sections:\n%s", got.Summary)
	}
	if goalPos < goalStart || goalPos > filesStart {
		t.Fatalf("原始目标未出现在 Goal 段（goalPos=%d, Goal=[%d,%d)）:\n%s",
			goalPos, goalStart, filesStart, got.Summary)
	}
}

func TestKeepFirstAndTail(t *testing.T) {
	tests := []struct {
		name  string
		items []string
		limit int
		want  []string
	}{
		{name: "empty", items: nil, limit: 5, want: nil},
		{name: "within limit unchanged", items: []string{"a", "b", "c"}, limit: 5, want: []string{"a", "b", "c"}},
		{name: "exactly limit unchanged", items: []string{"a", "b", "c", "d", "e"}, limit: 5, want: []string{"a", "b", "c", "d", "e"}},
		{name: "over limit keeps first and last four", items: []string{"a", "b", "c", "d", "e", "f"}, limit: 5, want: []string{"a", "c", "d", "e", "f"}},
		{name: "deduplicates first against tail", items: []string{"a", "b", "c", "d", "e", "a"}, limit: 5, want: []string{"a", "c", "d", "e"}},
		{name: "limit one keeps first", items: []string{"a", "b", "c"}, limit: 1, want: []string{"a"}},
		{name: "zero limit unchanged", items: []string{"a", "b", "c"}, limit: 0, want: []string{"a", "b", "c"}},
		{
			name:  "CJK graphemes intact",
			items: []string{"目标：词法分析器", "a", "b", "c", "d", "e", "末目标：发布"},
			limit: 5,
			want:  []string{"目标：词法分析器", "c", "d", "e", "末目标：发布"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := strings.Join(tt.items, "|")
			got := keepFirstAndTail(tt.items, tt.limit)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("keepFirstAndTail(%q, %d) = %q, want %q", tt.items, tt.limit, got, tt.want)
			}
			if after := strings.Join(tt.items, "|"); after != before {
				t.Fatalf("input mutated: before %q, after %q", before, after)
			}
			for _, s := range got {
				if !utf8.ValidString(s) {
					t.Errorf("invalid UTF-8 item %q", s)
				}
			}
		})
	}
}

func TestSummarizeGoalRetentionStableAndInputUnchanged(t *testing.T) {
	dropped := []Entry{{Role: "user", Content: "原始目标"}}
	for i := 0; i < 7; i++ {
		dropped = append(dropped, Entry{Role: "user", Content: fmt.Sprintf("合成轮 %d", i)})
	}
	before, _ := json.Marshal(dropped)
	want := Summarize(dropped, 0).Summary
	for n := 0; n < 50; n++ {
		if got := Summarize(dropped, 0).Summary; got != want {
			t.Fatalf("Summarize.Summary changed across runs:\n got %q\nwant %q", got, want)
		}
	}
	after, _ := json.Marshal(dropped)
	if string(before) != string(after) {
		t.Fatalf("Summarize mutated dropped input:\nbefore %s\nafter  %s", before, after)
	}
}

// containsItem 报告 items 是否包含 want（与 app 包的 containsLine 语义相同，但限定在本测试文件）。
func containsItem(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// assertCompactionChainFields 检查一次压缩载荷是否带齐四类结构，且指定条目不重复。
func assertCompactionChainFields(t *testing.T, c Compaction, goal, failure, next, file string) {
	t.Helper()
	if !strings.Contains(c.Summary, "## Goal\n") || !strings.Contains(c.Summary, goal) {
		t.Errorf("Goal 丢失（want %q）：\n%s", goal, c.Summary)
	}
	if !strings.Contains(c.Summary, failure) || !containsItem(c.Failures, failure) {
		t.Errorf("Failures 丢失（want %q）：Failures=%q\n%s", failure, c.Failures, c.Summary)
	}
	if !strings.Contains(c.Summary, next) || !containsItem(c.NextSteps, next) {
		t.Errorf("NextSteps 丢失（want %q）：NextSteps=%q\n%s", next, c.NextSteps, c.Summary)
	}
	if !containsItem(c.Files, file) {
		t.Errorf("Files 丢失（want %q）：%q", file, c.Files)
	}
	for _, bucket := range []struct {
		name  string
		items []string
	}{
		{"Files", c.Files}, {"Failures", c.Failures}, {"NextSteps", c.NextSteps},
	} {
		seen := make(map[string]bool, len(bucket.items))
		for _, item := range bucket.items {
			if seen[item] {
				t.Errorf("%s 出现重复条目 %q：%q", bucket.name, item, bucket.items)
			}
			seen[item] = true
		}
	}
}

// TestSummarizeAcrossCompactionChain 覆盖多次压缩链丢 Goal/Failures/NextSteps 的问题：
// Messages() 把旧压缩记录投影为 system Entry，第二次压缩的丢弃前缀里只有它的权威载荷。
// 新实现必须把 Goal（来自 Summary 的 ## Goal 段）与 Files/Failures/NextSteps（来自权威字段）
// 合并进新载荷，且两路来源重叠时不得产生重复项目符号。
func TestSummarizeAcrossCompactionChain(t *testing.T) {
	const (
		goal    = "原始目标：实现词法分析器"
		failure = "shell — error: undefined: token"
		next    = "Next steps: 补测试并跑 gofmt"
		file    = "lexer.go"
	)
	firstDropped := []Entry{
		{Role: "user", Content: goal},
		{Role: "assistant", Content: "已实现解析器"},
		{Role: "tool", Name: "shell", Content: "error: undefined: token"},
		pathEntry(file),
		{Role: "assistant", Content: next},
	}
	before, _ := json.Marshal(firstDropped)
	first := Summarize(firstDropped, 0)
	assertCompactionChainFields(t, first, goal, failure, next, file)

	// 旧压缩以 system Entry 重新进入 Messages() 投影；Data 就是其 Compaction 载荷。
	data, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDropped := []Entry{
		{Role: "system", Content: first.Summary, Data: data},
		{Role: "user", Content: "继续"},
		pathEntry(file), // 与旧压缩中的 File 重叠，验证去重
	}
	second := Summarize(secondDropped, 0)
	assertCompactionChainFields(t, second, goal, failure, next, file)

	// 原输入必须逐字节不变（含 system Entry 的 Data）。
	after, _ := json.Marshal(firstDropped)
	if string(before) != string(after) {
		t.Fatalf("Summarize 修改了 firstDropped 入参：\nbefore %s\nafter  %s", before, after)
	}
	if got, want := string(secondDropped[0].Data), string(data); got != want {
		t.Fatalf("Summarize 修改了 system Entry 的 Data：\n got %s\nwant %s", got, want)
	}

	// 同一份输入重复运行必须逐字节稳定。
	want := second.Summary
	for n := 0; n < 50; n++ {
		if got := Summarize(secondDropped, 0).Summary; got != want {
			t.Fatalf("重复压缩链摘要不稳定：\n got %q\nwant %q", got, want)
		}
	}
}

// TestAppendCompactionReemitsHiddenRetainedCompaction 覆盖主视图失效场景：旧压缩记录
// 带 RetainAlways，但被 Messages() 的 head=messageEntries(path[:latest]) 跳过，从不进入
// 派生的 entries。appendCompaction 必须从原始记录补发它，使其摘要文本重新出现在主视图。
func TestAppendCompactionReemitsHiddenRetainedCompaction(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.NewSession("hidden")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := s.Append(KindUser, fmt.Sprintf("m%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	// 直接追加压缩记录，模拟不经过 appendCompaction 的压缩路径。
	firstSummary := "## Compaction\n\n## Goal\n- 原始目标\n"
	if err := s.Append(KindCompaction, Compaction{Summary: firstSummary, Dropped: 4, Keep: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 5; i < 9; i++ {
		if err := s.Append(KindUser, fmt.Sprintf("m%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	secondSummary := "## Compaction\n\n## Goal\n- 第二轮目标\n"
	if err := s.Append(KindCompaction, Compaction{Summary: secondSummary, Dropped: 4, Keep: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 9; i < 11; i++ {
		if err := s.Append(KindUser, fmt.Sprintf("m%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	// 压缩前：只有最新的 c2 可见，c1 被 Messages() 跳过。
	for _, entry := range s.Messages() {
		if entry.Role == "system" && entry.Content == firstSummary {
			t.Fatal("旧压缩记录在压缩前就已出现在主视图，测试前提不成立")
		}
	}

	if err := s.Compact(1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	var sawFirst, sawSecond bool
	for _, entry := range s.Messages() {
		if entry.Role != "system" {
			continue
		}
		if entry.Content == firstSummary {
			sawFirst = true
		}
		if entry.Content == secondSummary {
			sawSecond = true
		}
	}
	if !sawFirst {
		t.Fatal("带 RetainAlways 的旧压缩记录未从原始记录补发到主视图")
	}
	if !sawSecond {
		t.Fatal("最新压缩记录未保留在主视图")
	}
}
