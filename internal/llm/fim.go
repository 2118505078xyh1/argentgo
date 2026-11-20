package llm

import (
	"context"
	"strings"
)

// fimTemplate describes how one model family writes a fill-in-the-middle
// request. The model sees the code before the cursor (prefix) and the code
// after it (suffix) woven together with family-specific control tokens, then
// continues generation from the infill point. stopIDs are the explicit
// end-of-infill / EOS token ids the upstream endpoint should stop on; they are
// empty for families that only use textual stop markers.
type fimTemplate struct {
	name    string
	aliases []string
	// begin opens the prompt, hole separates the two code halves, and end is
	// emitted after the second half: generation starts right after it.
	begin string
	hole  string
	end   string
	// order selects which half follows begin. Most families put the prefix
	// first; Codestral is the well-known exception (suffix first).
	order   string
	stopIDs []int64
}

// fimTemplates is ordered most-specific first. The last entry has no aliases
// and is the plain-text fallback for every unrecognised model name.
var fimTemplates = []fimTemplate{
	{
		name:    "qwen",
		aliases: []string{"qwen"},
		begin:   "<|fim_prefix|>",
		hole:    "<|fim_suffix|>",
		end:     "<|fim_middle|>",
		// Qwen2.5-Coder's base EOS, the documented FIM stop token.
		stopIDs: []int64{151643},
	},
	{
		name:    "deepseek",
		aliases: []string{"deepseek"},
		begin:   "<｜fim▁begin｜>",
		hole:    "<｜fim▁hole｜>",
		end:     "<｜fim▁end｜>",
		// <｜EOT｜> (fullwidth pipes) from the DeepSeek tool-conversion audit.
		stopIDs: []int64{128805},
	},
	{
		name:    "starcoder",
		aliases: []string{"starcoder", "bigcode"},
		begin:   "<fim_prefix>",
		hole:    "<fim_suffix>",
		end:     "<fim_middle>",
		// GPT-2 lineage: <|endoftext|> is id 0.
		stopIDs: []int64{0},
	},
	{
		name:    "codestral",
		aliases: []string{"codestral"},
		begin:   "[SUFFIX]",
		hole:    "[PREFIX]",
		order:   "suffix",
		// Mistral tokenizer EOS (</s>). Codestral also honours the textual
		// [PREFIX]/[SUFFIX] stops, which cannot be expressed as token ids.
		stopIDs: []int64{2},
	},
	{
		name:    "codellama",
		aliases: []string{"codellama", "code-llama"},
		begin:   "<PRE> ",
		hole:    " <SUF>",
		end:     " <MID>",
		// CodeLlama's <EOT> is id 0.
		stopIDs: []int64{0},
	},
	{
		// Plain-text fallback: no markers, so the two halves are simply
		// concatenated and no token ids are forced onto the request.
		name: "text",
	},
}

// fimTemplateFor resolves a model name to a template. Matching is
// case-insensitive and substring-based, so deployment prefixes (Ollama tags,
// "hf.co/..." paths, Hugging Face org names) still match. An empty or unknown
// name falls back to the plain-text template.
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

// render builds the FIM prompt for one template.
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

// RenderFIM renders a fill-in-the-middle prompt for model and returns the
// explicit stop token ids the endpoint should use. It is pure: no client, no
// network, and the returned slice is a copy safe for callers to mutate.
// Unknown model names use the plain-text fallback (prefix||suffix, no stops).
func RenderFIM(prefix, suffix, model string) (string, []int64) {
	t := fimTemplateFor(model)
	return t.render(prefix, suffix), append([]int64(nil), t.stopIDs...)
}

// FIMTemplateName reports which template RenderFIM would select for model. It
// is useful for diagnostics and for callers that want to expose the choice
// without duplicating the matching rules.
func FIMTemplateName(model string) string {
	return fimTemplateFor(model).name
}

// FillOptions tunes FillMiddle without changing Client.Chat's behaviour.
type FillOptions struct {
	// OnText receives the generated infill deltas, like Chat's onText.
	OnText func(string)
	// StopTokenIDs replaces the template's stop ids when non-empty. This lets
	// a caller override the model default from configuration.
	StopTokenIDs []int64
	// NoNetwork skips the HTTP request: FillMiddle then returns a Message whose
	// Content is the rendered FIM prompt, for callers that only need the wire
	// payload. Usage is nil and FinishReason is empty in that mode.
	NoNetwork bool
}

// FillMiddle renders a FIM prompt for model and asks the chat-completions
// endpoint to fill the gap between prefix and suffix. It reuses the same
// request/retry/stream pipeline as Chat, with the template's stop token ids
// attached as the optional stop_token_ids wire field. With opts.NoNetwork it
// performs construction only and reports the rendered prompt verbatim.
func (c *Client) FillMiddle(ctx context.Context, model, prefix, suffix string, opts FillOptions) (Message, error) {
	prompt, stopIDs := RenderFIM(prefix, suffix, model)
	if len(opts.StopTokenIDs) > 0 {
		stopIDs = append([]int64(nil), opts.StopTokenIDs...)
	}
	if opts.NoNetwork {
		return Message{Role: "user", Content: prompt}, nil
	}
	messages := []Message{{Role: "user", Content: prompt}}
	return c.chat(ctx, model, messages, nil, opts.OnText, stopIDs)
}
