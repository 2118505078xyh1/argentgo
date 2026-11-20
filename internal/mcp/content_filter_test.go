package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// textBlock 构造一个文本块，便于在过滤测试中只关注文本与 annotations。
func textBlock(text string, ann ContentBlockAnnotations) ContentBlock {
	return ContentBlock{Type: "text", Text: text, Annotations: ann}
}

func TestFilterContentBlocksAudience(t *testing.T) {
	// audience 不含 user 的块应被丢弃；含 user 或缺省的块应保留。
	blocks := []ContentBlock{
		textBlock("内部", ContentBlockAnnotations{Audience: []string{"assistant"}}),
		textBlock("给 User", ContentBlockAnnotations{Audience: []string{"user"}}),
		textBlock("两者", ContentBlockAnnotations{Audience: []string{"assistant", "user"}}),
		textBlock("缺省", ContentBlockAnnotations{}),
		textBlock("空", ContentBlockAnnotations{Audience: []string{}}),
		textBlock("中文角色", ContentBlockAnnotations{Audience: []string{"用户"}}),
	}
	got := filterContentBlocks(blocks, 0)
	want := []string{"给 User", "两者", "缺省", "空"}
	if len(got) != len(want) {
		t.Fatalf("保留数量不对：got %d blocks (%+v), want %d", len(got), got, len(want))
	}
	for i, text := range want {
		if got[i].Text != text {
			t.Fatalf("第 %d 块文本不对：got %q, want %q", i, got[i].Text, text)
		}
	}
}

func TestFilterContentBlocksPriority(t *testing.T) {
	p := func(v int) *int { return &v }
	blocks := []ContentBlock{
		textBlock("高", ContentBlockAnnotations{Priority: p(80)}),
		textBlock("低", ContentBlockAnnotations{Priority: p(30)}),
		textBlock("边界", ContentBlockAnnotations{Priority: p(50)}),
		textBlock("缺省", ContentBlockAnnotations{}),
	}
	// 阈值 50：30 被丢弃，80/50 保留，缺省视为最高优先级也保留。
	got := filterContentBlocks(blocks, 50)
	want := []string{"高", "边界", "缺省"}
	if len(got) != len(want) {
		t.Fatalf("保留数量不对：got %d (%+v), want %d", len(got), got, len(want))
	}
	for i, text := range want {
		if got[i].Text != text {
			t.Fatalf("第 %d 块文本不对：got %q, want %q", i, got[i].Text, text)
		}
	}
}

func TestFilterContentBlocksDefaultNoFilter(t *testing.T) {
	p := func(v int) *int { return &v }
	blocks := []ContentBlock{
		textBlock("一", ContentBlockAnnotations{}),
		textBlock("二", ContentBlockAnnotations{Priority: p(0)}),
		textBlock("三", ContentBlockAnnotations{Priority: p(1)}),
	}
	// minPriority 为 0 时，即便显式优先级为 0/1 也全部保留。
	got := filterContentBlocks(blocks, 0)
	if len(got) != len(blocks) {
		t.Fatalf("缺省配置不应过滤：got %d blocks, want %d", len(got), len(blocks))
	}
	// 全部保留时直接返回原切片，保证零拷贝、零行为变化。
	if &got[0] != &blocks[0] {
		t.Fatal("缺省配置应原样返回入参切片")
	}
}

func TestFilterContentBlocksCJK(t *testing.T) {
	p := func(v int) *int { return &v }
	// 含简体中文与 emoji 的文本过滤后必须逐字节保留，不得在字符中间截断。
	original := "你好，世界 🌏 — 工具输出"
	blocks := []ContentBlock{
		textBlock(original, ContentBlockAnnotations{Priority: p(90)}),
		textBlock("内部 🚫", ContentBlockAnnotations{Audience: []string{"assistant"}}),
	}
	got := filterContentBlocks(blocks, 50)
	if len(got) != 1 || got[0].Text != original {
		t.Fatalf("CJK 文本未原样保留：got %+v, want %q", got, original)
	}
}

func TestFilterContentBlocksEmpty(t *testing.T) {
	if got := filterContentBlocks(nil, 50); len(got) != 0 {
		t.Fatalf("nil 输入应得到空结果：%v", got)
	}
	if got := filterContentBlocks([]ContentBlock{}, 50); len(got) != 0 {
		t.Fatalf("空切片输入应得到空结果：%v", got)
	}
}

func TestContentBlockAnnotationsUnmarshal(t *testing.T) {
	// MCP 的 priority 是 0-1 浮点数，解析后放大为 0-100 的整数。
	var ann ContentBlockAnnotations
	if err := json.Unmarshal([]byte(`{"audience":["user"],"priority":0.5}`), &ann); err != nil {
		t.Fatalf("解析 annotations：%v", err)
	}
	if ann.Priority == nil || *ann.Priority != 50 {
		t.Fatalf("priority 归一化不对：%+v", ann.Priority)
	}
	if len(ann.Audience) != 1 || ann.Audience[0] != "user" {
		t.Fatalf("audience 解析不对：%+v", ann.Audience)
	}
	// 非法类型与 null 一律按未声明处理，不报错。
	for _, raw := range []string{`7`, `[]`, `{"audience":7,"priority":"x"}`, `{"priority":null}`} {
		var bad ContentBlockAnnotations
		if err := json.Unmarshal([]byte(raw), &bad); err != nil {
			t.Fatalf("坏 annotations %s 不应报错：%v", raw, err)
		}
		if bad.Priority != nil {
			t.Fatalf("坏 annotations %s 不应声明 priority：%d", raw, *bad.Priority)
		}
	}
}

func TestCallToolContentFilterOptIn(t *testing.T) {
	response := func() map[string]any {
		return map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "用户可见 内容"},
			map[string]any{"type": "text", "text": "内部 调试", "annotations": map[string]any{"audience": []string{"assistant"}}},
			map[string]any{"type": "text", "text": "低优先级 内容", "annotations": map[string]any{"priority": 0.2}},
		}}
	}
	newClient := func() *client {
		ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
			"tools/call": func(json.RawMessage) (any, error) { return response(), nil },
		}}
		return newFakeClient("fake", ft, time.Second)
	}

	// 默认关闭：输出与历史逐字节一致，三个块全部保留。
	t.Setenv(contentFilterMinPriorityEnv, "")
	got, err := newClient().CallTool(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("CallTool（默认）: %v", err)
	}
	want := "用户可见 内容\n内部 调试\n低优先级 内容"
	if got != want {
		t.Fatalf("默认配置输出变了：got %q, want %q", got, want)
	}

	// 选择加入：内部块与低优先级块被过滤，中文内容保持原样。
	t.Setenv(contentFilterMinPriorityEnv, "50")
	got, err = newClient().CallTool(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("CallTool（opt-in）: %v", err)
	}
	if got != "用户可见 内容" {
		t.Fatalf("opt-in 过滤结果不对：got %q", got)
	}
	if !strings.Contains(got, "用户可见") {
		t.Fatalf("CJK 内容丢失：%q", got)
	}
}
