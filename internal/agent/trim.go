package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argent-go/internal/llm"
)

const (
	// charsPerToken is the standard rule-of-thumb ratio for English and code.
	// It over-estimates CJK text, which is the safe direction for a budget.
	charsPerToken = 4
	// messageOverheadTokens covers the role marker and message framing every
	// provider adds to each entry.
	messageOverheadTokens = 4
	// trimmedPrefix marks a tool result whose body was reclaimed. One line
	// stays so the model still knows the call happened.
	trimmedPrefix = "[tool output trimmed:"
)

// EstimateTokens estimates the token cost of a string.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (utf8.RuneCountInString(text) + charsPerToken - 1) / charsPerToken
}

// EstimateMessage estimates one message, including its framing overhead.
func EstimateMessage(m llm.Message) int {
	n := messageOverheadTokens + EstimateTokens(m.Content) + EstimateTokens(m.Reasoning)
	for _, call := range m.ToolCalls {
		n += 2 + EstimateTokens(call.Function.Name) + EstimateTokens(call.Function.Arguments)
	}
	return n
}

// EstimateMessages estimates a whole conversation.
func EstimateMessages(messages []llm.Message) int {
	total := 0
	for _, m := range messages {
		total += EstimateMessage(m)
	}
	return total
}

// EstimateTools estimates the tool schemas sent with each request.
func EstimateTools(specs []llm.ToolSpec) int {
	total := 0
	for _, spec := range specs {
		total += messageOverheadTokens +
			EstimateTokens(spec.Function.Name) +
			EstimateTokens(spec.Function.Description) +
			EstimateTokens(string(spec.Function.Parameters))
	}
	return total
}

// TrimOptions controls context trimming.
type TrimOptions struct {
	// MaxTokens is the target size. Zero disables trimming.
	MaxTokens int
	// KeepTurns is how many trailing assistant turns stay verbatim.
	KeepTurns int
	// KeepToolTokens is how much of the most recent tool output stays
	// verbatim. Older output is replaced by a one-line stub.
	KeepToolTokens int
}

// TrimResult reports what trimming did.
type TrimResult struct {
	Messages []llm.Message
	// Pruned is how many tool results were stubbed.
	Pruned int
	// Freed is the estimated token saving.
	Freed int
}

// Trim shrinks a conversation by replacing old tool output with a one-line
// stub. It never removes a message, so tool calls keep their results paired.
//
// Always kept verbatim: every system message, the first user message, the last
// KeepTurns turns, and the most recent KeepToolTokens worth of tool output.
// Older tool bodies are the cheapest thing to reclaim — the call and its status
// stay visible, so the model still knows what was done.
func Trim(messages []llm.Message, opts TrimOptions) TrimResult {
	out := append([]llm.Message(nil), messages...)
	res := TrimResult{Messages: out}
	if opts.MaxTokens <= 0 {
		return res
	}
	total := EstimateMessages(out)
	if total <= opts.MaxTokens {
		return res
	}

	keepTurns := opts.KeepTurns
	if keepTurns <= 0 {
		keepTurns = 6
	}
	keepToolTokens := opts.KeepToolTokens
	if keepToolTokens <= 0 {
		keepToolTokens = 2000
	}

	protected := protectedIndexes(out, keepTurns)
	recentTool := keepToolTokens
	for i := len(out) - 1; i >= 0; i-- {
		msg := out[i]
		if msg.Role != "tool" || protected[i] {
			continue
		}
		cost := EstimateMessage(msg)
		if recentTool > 0 {
			recentTool -= cost
			continue
		}
		if msg.Content == "" || strings.HasPrefix(msg.Content, trimmedPrefix) {
			continue
		}
		stub := toolStub(len([]rune(msg.Content)))
		stubbed := msg
		stubbed.Content = stub
		freed := cost - EstimateMessage(stubbed)
		if freed <= 0 {
			continue
		}
		out[i] = stubbed
		res.Pruned++
		res.Freed += freed
		total -= freed
		if total <= opts.MaxTokens {
			break
		}
	}
	return res
}

// protectedIndexes marks messages trimming must not touch.
func protectedIndexes(messages []llm.Message, keepTurns int) []bool {
	protected := make([]bool, len(messages))
	for i, msg := range messages {
		if msg.Role == "system" {
			protected[i] = true
		}
	}
	for i, msg := range messages {
		if msg.Role == "user" {
			protected[i] = true
			break
		}
	}
	seen := 0
	start := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		seen++
		start = i
		if seen >= keepTurns {
			break
		}
	}
	for i := start; i < len(messages); i++ {
		protected[i] = true
	}
	return protected
}

// toolStub is the one-line replacement for a pruned tool result.
func toolStub(chars int) string {
	return fmt.Sprintf("%s %d chars of this tool result were removed to save context; run the tool again if the details matter]", trimmedPrefix, chars)
}
