package session

import (
	"reflect"
	"strings"
	"testing"
)

// v2Fields 把扩展摘要的九个桶按字段名展开，便于表驱动断言"只落到正确的桶"。
func v2Fields(s StructuredSummaryV2) map[string][]string {
	return map[string][]string{
		"Objective":        s.Objective,
		"Requirements":     s.Requirements,
		"Decisions":        s.Decisions,
		"WorkState":        s.WorkState,
		"RelevantFiles":    s.RelevantFiles,
		"ImportantContext": s.ImportantContext,
		"Progress":         s.Progress,
		"Uncertainties":    s.Uncertainties,
		"UserActions":      s.UserActions,
	}
}

func TestParseStructuredSummaryV2HeadingMappings(t *testing.T) {
	// 每个标题（英文 + 中文）必须把条目解析到唯一正确的桶。
	cases := []struct {
		heading string
		field   string
	}{
		// Objective。
		{"Objective", "Objective"},
		{"Objective(s)", "Objective"},
		{"目标", "Objective"},
		{"目的", "Objective"},
		// Requirements。
		{"requirements", "Requirements"},
		{"Requirement", "Requirements"},
		{"需求", "Requirements"},
		// Decisions。
		{"Decisions", "Decisions"},
		{"decision", "Decisions"},
		{"决策", "Decisions"},
		// Work State。
		{"work state", "WorkState"},
		{"Workstate", "WorkState"},
		{"Working state", "WorkState"},
		{"工作区状态", "WorkState"},
		{"工作进度", "WorkState"},
		// Relevant Files。
		{"Relevant files", "RelevantFiles"},
		{"related files", "RelevantFiles"},
		{"相关文件", "RelevantFiles"},
		// Important Context。
		{"Important context", "ImportantContext"},
		{"important", "ImportantContext"},
		{"重要上下文", "ImportantContext"},
		{"重要信息", "ImportantContext"},
		// 原有三桶的标题保持兼容。
		{"Progress", "Progress"},
		{"Uncertainties", "Uncertainties"},
		{"User actions", "UserActions"},
		{"Goal", "Progress"},
		{"failures", "Uncertainties"},
		{"next steps", "UserActions"},
	}
	for _, tc := range cases {
		t.Run(tc.heading, func(t *testing.T) {
			text := "## " + tc.heading + "\n- 唯一条目\n"
			got, ok := ParseStructuredSummaryV2(text)
			if !ok {
				t.Fatalf("标题 %q 未被识别", tc.heading)
			}
			fields := v2Fields(got)
			for name, items := range fields {
				if name == tc.field {
					if !reflect.DeepEqual(items, []string{"唯一条目"}) {
						t.Errorf("%s = %q, want [唯一条目] (heading %q)", name, items, tc.heading)
					}
				} else if len(items) != 0 {
					t.Errorf("非目标桶 %s = %q, want empty (heading %q)", name, items, tc.heading)
				}
			}
		})
	}
}

func TestRenderStructuredSummaryV2RoundTrip(t *testing.T) {
	want := StructuredSummaryV2{
		Objective:        []string{"修复解析 bug"},
		Requirements:     []string{"保持字节不变", "stdlib-only"},
		Decisions:        []string{"新增独立类型"},
		WorkState:        []string{"核心已完成"},
		RelevantFiles:    []string{"compact_fields.go"},
		ImportantContext: []string{"默认关闭"},
		Progress:         []string{"表驱动测试"},
		Uncertainties:    []string{"交叉编译待验证"},
		UserActions:      []string{"运行 go test"},
	}

	text := RenderStructuredSummaryV2(want)
	for _, heading := range []string{
		"## Objective", "## Requirements", "## Decisions", "## Work State",
		"## Relevant Files", "## Important Context", "## Progress",
		"## Uncertainties", "## User actions",
	} {
		if !strings.Contains(text, heading) {
			t.Errorf("渲染文本缺少 %q:\n%s", heading, text)
		}
	}
	got, ok := ParseStructuredSummaryV2(text)
	if !ok {
		t.Fatalf("渲染文本无法解析:\n%s", text)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("往返不一致:\n got %#v\nwant %#v\ntext:\n%s", got, want, text)
	}

	// Parse -> Render -> Parse 同样一致。
	again, ok := ParseStructuredSummaryV2(RenderStructuredSummaryV2(got))
	if !ok || !reflect.DeepEqual(again, got) {
		t.Fatalf("Parse->Render->Parse 不一致: ok=%v\n got %#v\nwant %#v", ok, again, got)
	}
}

func TestRenderStructuredSummaryV2EmptyFields(t *testing.T) {
	text := RenderStructuredSummaryV2(StructuredSummaryV2{})
	if strings.Count(text, "- (none)") != 9 {
		t.Fatalf("空字段占位符数量不是 9:\n%s", text)
	}
	got, ok := ParseStructuredSummaryV2(text)
	if !ok {
		t.Fatalf("占位符文本无法解析:\n%s", text)
	}
	for name, items := range v2Fields(got) {
		if len(items) != 0 {
			t.Errorf("空字段 %s 往返后为 %q, want empty", name, items)
		}
	}
}

func TestParseStructuredSummaryV2UnknownHeading(t *testing.T) {
	// 无标题、格式漂移、未知标题都必须回退为 (zero, false)。
	cases := []string{
		"",
		" \n\t\n",
		"just prose about objective\n- 条目\n",
		"Objective:\n- 条目\n",
		"**Objective**\n- 条目\n",
		"## Compaction\n- dropped 1 message(s)\n",
		"## Unknown heading\n- 条目\n",
		"## Objective update\n- 条目\n",
	}
	for _, text := range cases {
		got, ok := ParseStructuredSummaryV2(text)
		if ok {
			t.Errorf("格式漂移被误判为结构化: %q -> %#v", text, got)
		}
		if !reflect.DeepEqual(got, StructuredSummaryV2{}) {
			t.Errorf("失败解析必须返回零值 DTO: %q -> %#v", text, got)
		}
	}
}

func TestParseStructuredSummaryV2Dedupe(t *testing.T) {
	text := "## Objective\n" +
		"- 目标 A\n" +
		"- (none)\n" +
		"- 目标 B\n" +
		"- 目标 A\n" +
		"- 无\n"
	got, ok := ParseStructuredSummaryV2(text)
	if !ok {
		t.Fatalf("已识别标题未被解析")
	}
	if want := []string{"目标 A", "目标 B"}; !reflect.DeepEqual(got.Objective, want) {
		t.Fatalf("Objective = %q, want %q (必须按出现顺序去重)", got.Objective, want)
	}
}

func TestParseStructuredSummaryV2CRLF(t *testing.T) {
	text := "## Objective\r\n" +
		"- 目标条目\r\n" +
		"\r\n" +
		"## 需求\r\n" +
		"- 需求条目\r\n"
	got, ok := ParseStructuredSummaryV2(text)
	if !ok {
		t.Fatalf("CRLF 输入未被识别")
	}
	if !reflect.DeepEqual(got.Objective, []string{"目标条目"}) {
		t.Errorf("Objective = %q, want [目标条目]", got.Objective)
	}
	if !reflect.DeepEqual(got.Requirements, []string{"需求条目"}) {
		t.Errorf("Requirements = %q, want [需求条目]", got.Requirements)
	}
}

func TestStructuredSummaryV2CoexistsWithLegacyThreeBuckets(t *testing.T) {
	text := "## Objective\n- 目标\n\n" +
		"## Requirements\n- 需求\n\n" +
		"## Decisions\n- 决策\n\n" +
		"## Work State\n- 状态\n\n" +
		"## Relevant Files\n- 文件\n\n" +
		"## Important Context\n- 上下文\n\n" +
		"## Progress\n- 进度\n\n" +
		"## Uncertainties\n- 不确定\n\n" +
		"## User actions\n- 用户操作\n"

	v2, ok := ParseStructuredSummaryV2(text)
	if !ok {
		t.Fatalf("V2 解析失败")
	}
	for name, items := range v2Fields(v2) {
		if len(items) != 1 {
			t.Errorf("V2 桶 %s = %q, want 1 项", name, items)
		}
	}

	// 同一段文本仍可被原解析器消费，三个旧桶不受影响（逐字节不变的前提）。
	legacy, ok := ParseStructuredSummary(text)
	if !ok {
		t.Fatalf("原解析器无法消费同一段文本")
	}
	want := StructuredSummary{
		Progress:      []string{"进度"},
		Uncertainties: []string{"不确定"},
		UserActions:   []string{"用户操作"},
	}
	if !reflect.DeepEqual(legacy, want) {
		t.Fatalf("原三桶被影响:\n got %#v\nwant %#v", legacy, want)
	}
}

func TestRenderStructuredSummaryV2NormalizesLineBreaks(t *testing.T) {
	s := StructuredSummaryV2{
		Objective: []string{"跨行\r\n条目"},
	}
	text := RenderStructuredSummaryV2(s)
	for _, line := range strings.Split(text, "\n") {
		if strings.ContainsRune(line, '\r') {
			t.Errorf("渲染行仍含 \\r: %q", line)
		}
	}
	got, ok := ParseStructuredSummaryV2(text)
	if !ok {
		t.Fatalf("归一化后的文本无法解析:\n%s", text)
	}
	// 借鉴现有实现：先 \r 再 \n 各替换为空格，因此 CRLF 会产生两个空格。
	if want := []string{"跨行  条目"}; !reflect.DeepEqual(got.Objective, want) {
		t.Fatalf("Objective = %q, want %q", got.Objective, want)
	}
}

func TestCompactionStructuredV2IsReadOnlyProjection(t *testing.T) {
	c := Compaction{
		Summary: "## Objective\n- 修复 bug\n",
		Dropped: 3,
		Keep:    2,
		Files:   []string{"a.go"},
	}
	got, ok := c.StructuredV2()
	if !ok {
		t.Fatalf("Compaction.StructuredV2 返回 false")
	}
	if !reflect.DeepEqual(got.Objective, []string{"修复 bug"}) {
		t.Fatalf("Objective = %q, want [修复 bug]", got.Objective)
	}
	// 只读投影：底层 Compaction 完全不变。
	if c.Summary != "## Objective\n- 修复 bug\n" {
		t.Fatalf("StructuredV2 修改了 Summary: %q", c.Summary)
	}
	if c.Dropped != 3 || c.Keep != 2 || !reflect.DeepEqual(c.Files, []string{"a.go"}) {
		t.Fatalf("StructuredV2 修改了 Compaction 其他字段: %#v", c)
	}
}
