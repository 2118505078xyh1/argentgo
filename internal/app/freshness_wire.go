package app

import (
	"context"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// readBeforeWriteEnv 是「读后写新鲜度校验」的独立门控变量。
const readBeforeWriteEnv = "ARGENT_READ_BEFORE_WRITE"

func readBeforeWriteEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(readBeforeWriteEnv)) == "1"
}

// freshnessTools 在 opt-in 开启时把本次会话的文件新鲜度账本注入工具调用的 ctx。
// 包装器遵循 sourceAnnotatingTools 的转发契约：Specs 与 Guarded 必须转发，
// 否则会破坏「守卫先于审批」的语义（agent 会再问一次审批器）。
type freshnessTools struct {
	inner  agent.ToolRunner
	ledger *tools.FileFreshness
}

var _ agent.ToolRunner = (*freshnessTools)(nil)

func (f *freshnessTools) Specs() []llm.ToolSpec { return f.inner.Specs() }

func (f *freshnessTools) Guarded() bool {
	if g, ok := f.inner.(interface{ Guarded() bool }); ok {
		return g.Guarded()
	}
	return false
}

func (f *freshnessTools) Run(ctx context.Context, name, args string) (string, error) {
	return f.inner.Run(tools.WithFileFreshness(ctx, f.ledger), name, args)
}
