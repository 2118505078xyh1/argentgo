package app

import (
	"context"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

type fakeToolRunner struct {
	out string
	err error
}

func (f *fakeToolRunner) Specs() []llm.ToolSpec { return nil }
func (f *fakeToolRunner) Run(context.Context, string, string) (string, error) {
	return f.out, f.err
}

// TestSourceAnnotatingToolsAddsSource 验证接线 wrapper 对 web_fetch 成功结果
// 加来源标注，且标注出现在正文之前。
func TestSourceAnnotatingToolsAddsSource(t *testing.T) {
	inner := &fakeToolRunner{out: "page body"}
	w := &sourceAnnotatingTools{inner: inner}
	got, err := w.Run(context.Background(), "web_fetch", `{"url":"https://example.com/p"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "[source: ") {
		t.Fatalf("来源标注缺失: %q", got)
	}
	if !strings.Contains(got, "https://example.com/p") {
		t.Fatalf("来源 URL 缺失: %q", got)
	}
	if !strings.HasSuffix(got, "page body") {
		t.Fatalf("正文被改写: %q", got)
	}
}

// TestSourceAnnotatingToolsLeavesOthersUntouched 验证其它工具与非成功结果逐字节不变。
func TestSourceAnnotatingToolsLeavesOthersUntouched(t *testing.T) {
	w := &sourceAnnotatingTools{inner: &fakeToolRunner{out: "unchanged"}}
	got, _ := w.Run(context.Background(), "read", `{"path":"a.go"}`)
	if got != "unchanged" {
		t.Fatalf("非 web 工具被改写: %q", got)
	}
	w2 := &sourceAnnotatingTools{inner: &fakeToolRunner{out: "partial", err: context.Canceled}}
	got, err := w2.Run(context.Background(), "web_fetch", `{"url":"https://example.com"}`)
	if got != "partial" || err != context.Canceled {
		t.Fatalf("失败结果被改写: out=%q err=%v", got, err)
	}
}
