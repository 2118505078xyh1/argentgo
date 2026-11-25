package app

import (
	"context"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/llm"
)

// sourceAnnotatingTools 在工具结果返回给模型前叠加来源 URL 标注。
// 它独立于 hooks bridge：无论是否配置了 hook 文档，来源标注都生效，
// 从而兑现 source_url.go 里 annotateSourceURL 这个原本零调用方的原语。
// 仅改写 web_fetch/web_search 的成功输出（前面加一行已脱敏来源），
// 其它工具、失败结果与无 URL 参数逐字节不变。
type sourceAnnotatingTools struct {
	inner agent.ToolRunner
}

func (s *sourceAnnotatingTools) Specs() []llm.ToolSpec { return s.inner.Specs() }

// Guarded 转发内层工具集的守卫标记。agent 循环据此判断安全链是否已在工具层
// 运行（避免双重审批）；包装器若丢失该接口，会让 agent 在 plan/策略守卫之前
// 再问一次审批器，破坏 plan 模式等"守卫先于审批"的语义。
func (s *sourceAnnotatingTools) Guarded() bool {
	if g, ok := s.inner.(interface{ Guarded() bool }); ok {
		return g.Guarded()
	}
	return false
}

func (s *sourceAnnotatingTools) Run(ctx context.Context, name, args string) (string, error) {
	out, err := s.inner.Run(ctx, name, args)
	if err != nil {
		return out, err
	}
	return annotateSourceURL(name, args, out), nil
}
