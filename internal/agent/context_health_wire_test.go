package agent

import (
	"reflect"
	"testing"
)

func TestContextHealthFromMessagesProjectsContentOnly(t *testing.T) {
	entries := []MessageSample{
		{Role: "user", Content: "请读取文件"},
		{Role: "assistant", Content: "[tool output trimmed: 5000 chars] 后续"},
		{Role: "tool", Content: "普通内容"},
	}
	got := ContextHealthFromMessages(entries)
	// 直接对“仅 Content”求值，适配层结果必须与之一致，说明 Role 默认不参与筛选。
	want := ScoreContextHealth([]string{
		"请读取文件",
		"[tool output trimmed: 5000 chars] 后续",
		"普通内容",
	})
	if got != want {
		t.Fatalf("ContextHealthFromMessages = %+v, want %+v", got, want)
	}
	// ScoreContextHealth 固定到小数后 6 位，故以它的结果为准而非未舍入的 1/3。
	if !nearHealth(got.ErrorStubRatio, want.ErrorStubRatio) {
		t.Fatalf("ErrorStubRatio = %v, want %v", got.ErrorStubRatio, want.ErrorStubRatio)
	}
	if !nearHealth(got.Score, want.Score) {
		t.Fatalf("Score = %v, want %v", got.Score, want.Score)
	}
}

func TestContextHealthFromMessagesRoleIgnoredByDefault(t *testing.T) {
	contents := []MessageSample{
		{Role: "user", Content: "guidance: retry"},
		{Role: "assistant", Content: "guidance: retry"},
	}
	renamed := []MessageSample{
		{Role: "system", Content: "guidance: retry"},
		{Role: "tool", Content: "guidance: retry"},
	}
	got := ContextHealthFromMessages(contents)
	other := ContextHealthFromMessages(renamed)
	if got != other {
		t.Fatalf("默认应忽略 Role：%+v vs %+v", got, other)
	}
}

func TestContextHealthFromMessagesWithFilterInclude(t *testing.T) {
	entries := []MessageSample{
		{Role: "  USER  ", Content: "guidance: retry"},
		{Role: "assistant", Content: "guidance: retry"},
		{Role: "tool", Content: "[truncated] stub"},
	}
	// 包含过滤：归一化后的 "user" 命中带空白的 "  USER  "，仅保留首条。
	got := ContextHealthFromMessagesWithFilter(entries, ContextHealthRoleFilter{
		IncludeRoles: []string{"user"},
	})
	want := ScoreContextHealth([]string{"guidance: retry"})
	if got != want {
		t.Fatalf("Include 过滤 = %+v, want %+v", got, want)
	}
	if got.Score != 1 {
		t.Fatalf("仅一条指引无重复、无 stub，Score 应为 1，got %v", got.Score)
	}
}

func TestContextHealthFromMessagesWithFilterExclude(t *testing.T) {
	entries := []MessageSample{
		{Role: "user", Content: "guidance: retry"},
		{Role: "assistant", Content: "guidance: retry"},
		{Role: "tool", Content: "[truncated] stub"},
	}
	// 排除 tool 后剩两条相同指引 → 1 次重复 / 2 = 0.5，无 stub。
	got := ContextHealthFromMessagesWithFilter(entries, ContextHealthRoleFilter{
		ExcludeRoles: []string{" TOOL "},
	})
	if w := 0.5; !nearHealth(got.GuidanceRepeatRatio, w) {
		t.Fatalf("GuidanceRepeatRatio = %v, want %v", got.GuidanceRepeatRatio, w)
	}
	if got.ErrorStubRatio != 0 {
		t.Fatalf("ErrorStubRatio = %v, want 0", got.ErrorStubRatio)
	}
	if w := 0.7; !nearHealth(got.Score, w) {
		t.Fatalf("Score = %v, want %v", got.Score, w)
	}
}

func TestContextHealthFromMessagesWithFilterExcludeWins(t *testing.T) {
	entries := []MessageSample{
		{Role: "user", Content: "guidance: retry"},
		{Role: "assistant", Content: "[truncated] stub"},
	}
	got := ContextHealthFromMessagesWithFilter(entries, ContextHealthRoleFilter{
		IncludeRoles: []string{"user", "assistant"},
		ExcludeRoles: []string{"assistant"},
	})
	want := ScoreContextHealth([]string{"guidance: retry"})
	if got != want {
		t.Fatalf("排除应优先于包含：got %+v, want %+v", got, want)
	}
}

func TestContextHealthFromMessagesWithFilterEmptyResult(t *testing.T) {
	entries := []MessageSample{{Role: "user", Content: "[truncated] guidance: retry"}}
	got := ContextHealthFromMessagesWithFilter(entries, ContextHealthRoleFilter{
		ExcludeRoles: []string{"user"},
	})
	if want := (ContextHealth{Score: 1}); got != want {
		t.Fatalf("全部被过滤应按空输入返回满分：got %+v, want %+v", got, want)
	}
}

func TestContextHealthFromMessagesCJKAndEmojiSafe(t *testing.T) {
	entries := []MessageSample{
		{Role: "user", Content: "指引：请先诊断失败原因，再改用其他工具 🛠️"},
		{Role: "assistant", Content: "指引：请先诊断失败原因，再改用其他工具 🛠️"},
		{Role: "tool", Content: "中文内容完全不同，不含任何标记"},
		{Role: "tool", Content: "输出被裁剪 [truncated] 后面还有中文"},
	}
	got := ContextHealthFromMessages(entries)
	if w := 0.5; !nearHealth(got.GuidanceRepeatRatio, w) {
		t.Fatalf("GuidanceRepeatRatio = %v, want %v", got.GuidanceRepeatRatio, w)
	}
	if w := 0.25; !nearHealth(got.ErrorStubRatio, w) {
		t.Fatalf("ErrorStubRatio = %v, want %v", got.ErrorStubRatio, w)
	}
	if w := 0.575; !nearHealth(got.Score, w) {
		t.Fatalf("Score = %v, want %v", got.Score, w)
	}
}

func TestContextHealthFromMessagesDoesNotMutateInput(t *testing.T) {
	entries := []MessageSample{
		{Role: " user ", Content: "[truncated] guidance: retry"},
		{Role: "tool", Content: "guidance: retry"},
	}
	original := append([]MessageSample(nil), entries...)

	ContextHealthFromMessages(entries)
	ContextHealthFromMessagesWithFilter(entries, ContextHealthRoleFilter{
		IncludeRoles: []string{"user"},
		ExcludeRoles: []string{"tool"},
	})

	if !reflect.DeepEqual(entries, original) {
		t.Fatalf("入参被修改: got %+v, want %+v", entries, original)
	}
}

func TestContextHealthFromMessagesDeterministic(t *testing.T) {
	entries := []MessageSample{
		{Role: "user", Content: "guidance: change the tool before retrying"},
		{Role: "assistant", Content: "guidance: change the tool before retrying"},
		{Role: "tool", Content: "[tool output trimmed] x"},
		{Role: "assistant", Content: "无关内容"},
	}
	filter := ContextHealthRoleFilter{ExcludeRoles: []string{"user"}}
	first := ContextHealthFromMessages(entries)
	firstFiltered := ContextHealthFromMessagesWithFilter(entries, filter)
	for i := 0; i < 20; i++ {
		if got := ContextHealthFromMessages(entries); got != first {
			t.Fatalf("第 %d 次调用 = %+v, want %+v", i, got, first)
		}
		if got := ContextHealthFromMessagesWithFilter(entries, filter); got != firstFiltered {
			t.Fatalf("第 %d 次带过滤调用 = %+v, want %+v", i, got, firstFiltered)
		}
	}
}

func TestContextHealthLoggerDefaultNilAndOptIn(t *testing.T) {
	// 默认无钩子：验证初始状态并恢复，避免影响其它测试。
	if fn := currentContextHealthLogger(); fn != nil {
		t.Fatalf("默认日志钩子应为 nil，got %v", fn != nil)
	}
	t.Cleanup(func() { SetContextHealthLogger(nil) })

	entries := []MessageSample{
		{Role: "user", Content: "guidance: retry"},
		{Role: "user", Content: "guidance: retry"},
	}
	got := ContextHealthFromMessages(entries)
	// 未设置钩子时调用不应产生任何输出（无 panic 即通过）。

	var captured []ContextHealth
	SetContextHealthLogger(func(h ContextHealth) { captured = append(captured, h) })
	gotWithLogger := ContextHealthFromMessages(entries)
	if len(captured) != 1 {
		t.Fatalf("钩子调用次数 = %d, want 1", len(captured))
	}
	if captured[0] != gotWithLogger {
		t.Fatalf("钩子收到 %+v, want %+v", captured[0], gotWithLogger)
	}
	if gotWithLogger != got {
		t.Fatalf("设置钩子不应改变评分：%v vs %v", gotWithLogger, got)
	}

	// 传入 nil 关闭钩子。
	SetContextHealthLogger(nil)
	if fn := currentContextHealthLogger(); fn != nil {
		t.Fatal("SetContextHealthLogger(nil) 后钩子应关闭")
	}
}
