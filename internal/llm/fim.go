package llm

import (
	"context"
	"strings"
)

// fimTemplate 描述某个模型系列如何编写中间填充（fill-in-the-middle）
// 请求。模型看到光标前的代码（prefix）和之后的
// 代码（suffix），用该系列特有的控制 token 编织在一起，然后
// 从填充点继续生成。stopIDs 是上游端点应停止的显式
// 填充结束/EOS token id；对于只使用文本停止标记的系列，
// 它们为空。
type fimTemplate struct {
	name    string
	aliases []string
	// begin 开启提示，hole 分隔两段代码，end 在
	// 第二段之后发出：生成紧接着它开始。
	begin string
	hole  string
	end   string
	// order 选择哪一段跟在 begin 之后。大多数系列把 prefix
	// 放在前面；Codestral 是众所周知的例外（suffix 在前）。
	order   string
	stopIDs []int64
}

// fimTemplates 按最具体到最不具体的顺序排列。最后一项没有别名，
// 是所有无法识别的模型名的纯文本回退。
var fimTemplates = []fimTemplate{
	{
		name:    "qwen",
		aliases: []string{"qwen"},
		begin:   "<|fim_prefix|>",
		hole:    "<|fim_suffix|>",
		end:     "<|fim_middle|>",
		// Qwen2.5-Coder 的基础 EOS，文档记录的 FIM 停止 token。
		stopIDs: []int64{151643},
	},
	{
		name:    "deepseek",
		aliases: []string{"deepseek"},
		begin:   "<｜fim▁begin｜>",
		hole:    "<｜fim▁hole｜>",
		end:     "<｜fim▁end｜>",
		// 来自 DeepSeek 工具转换审计的 <｜EOT｜>（全角竖线）。
		stopIDs: []int64{128805},
	},
	{
		name:    "starcoder",
		aliases: []string{"starcoder", "bigcode"},
		begin:   "<fim_prefix>",
		hole:    "<fim_suffix>",
		end:     "<fim_middle>",
		// GPT-2 系列：<|endoftext|> 的 id 为 0。
		stopIDs: []int64{0},
	},
	{
		name:    "codestral",
		aliases: []string{"codestral"},
		begin:   "[SUFFIX]",
		hole:    "[PREFIX]",
		order:   "suffix",
		// Mistral 分词器 EOS（</s>）。Codestral 也识别文本
		// [PREFIX]/[SUFFIX] 停止标记，它们无法用 token id 表达。
		stopIDs: []int64{2},
	},
	{
		name:    "codellama",
		aliases: []string{"codellama", "code-llama"},
		begin:   "<PRE> ",
		hole:    " <SUF>",
		end:     " <MID>",
		// CodeLlama 的 <EOT> 的 id 为 0。
		stopIDs: []int64{0},
	},
	{
		// 纯文本回退：没有标记，因此两段代码直接
		// 拼接，不向请求强制添加任何 token id。
		name: "text",
	},
}

// fimTemplateFor 把模型名解析为模板。匹配
// 不区分大小写且基于子串，因此部署前缀（Ollama 标签、
// "hf.co/..." 路径、Hugging Face 组织名）仍能匹配。空名或未知
// 名称回退到纯文本模板。
func fimTemplateFor(model string) fimTemplate {
	lower := strings.ToLower(model)
	for _, t := range fimTemplates {
		for _, alias := range t.aliases {
			if strings.Contains(lower, alias) {
				return t
			}
		}
	}
	return fimTemplates[len(fimTemplates)-1]
}

// render 为一个模板构建 FIM 提示。
func (t fimTemplate) render(prefix, suffix string) string {
	var b strings.Builder
	b.WriteString(t.begin)
	if t.order == "suffix" {
		b.WriteString(suffix)
		b.WriteString(t.hole)
		b.WriteString(prefix)
	} else {
		b.WriteString(prefix)
		b.WriteString(t.hole)
		b.WriteString(suffix)
	}
	b.WriteString(t.end)
	return b.String()
}

// RenderFIM 为 model 渲染一个中间填充提示，并返回端点应使用的
// 显式停止 token id。它是纯函数：无客户端、无
// 网络，且返回的切片是副本，调用方可安全修改。
// 未知模型名使用纯文本回退（prefix||suffix，无停止标记）。
func RenderFIM(prefix, suffix, model string) (string, []int64) {
	t := fimTemplateFor(model)
	return t.render(prefix, suffix), append([]int64(nil), t.stopIDs...)
}

// FIMTemplateName 报告 RenderFIM 会为 model 选择哪个模板。
// 它有助于诊断，也适合那些想在不重复匹配规则的前提下
// 暴露该选择的调用方。
func FIMTemplateName(model string) string {
	return fimTemplateFor(model).name
}

// FillOptions 调整 FillMiddle，而不改变 Client.Chat 的行为。
type FillOptions struct {
	// OnText 接收生成的填充增量，类似 Chat 的 onText。
	OnText func(string)
	// StopTokenIDs 非空时替换模板的停止 id。这让
	// 调用方可以从配置中覆盖模型默认值。
	StopTokenIDs []int64
	// NoNetwork 跳过 HTTP 请求：此时 FillMiddle 返回一个 Message，其
	// Content 是渲染后的 FIM 提示，供只需要线协议
	// 载荷的调用方使用。该模式下 Usage 为 nil、FinishReason 为空。
	NoNetwork bool
}

// FillMiddle 为 model 渲染 FIM 提示，并要求 chat-completions
// 端点填充 prefix 与 suffix 之间的空白。它复用与 Chat 相同的
// 请求/重试/流式管线，并把模板的停止 token id 作为可选的
// stop_token_ids 线协议字段附加。设置 opts.NoNetwork 时，
// 它只做构造，并原样报告渲染后的提示。
func (c *Client) FillMiddle(ctx context.Context, model, prefix, suffix string, opts FillOptions) (Message, error) {
	prompt, stopIDs := RenderFIM(prefix, suffix, model)
	if len(opts.StopTokenIDs) > 0 {
		stopIDs = append([]int64(nil), opts.StopTokenIDs...)
	}
	if opts.NoNetwork {
		return Message{Role: "user", Content: prompt}, nil
	}
	messages := []Message{{Role: "user", Content: prompt}}
	// FIM 永不发送推理参数：末位传空串，即使客户端设置了 Options.Thinking
	// 也不派生 reasoning_effort，保持 FIM 请求字节与接线前一致。
	return c.chat(ctx, model, messages, nil, opts.OnText, stopIDs, "", 0)
}
