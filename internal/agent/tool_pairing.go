package agent

import (
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// Tool results on the neutral transcript are Role:"tool" messages keyed by
// ToolCallID. llm.Message carries no name or error flag for a result, so a
// missing name is preserved in the result text and an error result is marked
// with the same "error: " prefix the live loop uses (see dispatch.go).
const (
	missingToolResultText = "Tool result missing"
	emptyToolResultText   = "(empty tool result)"
	unknownToolName       = "unknown"
	unknownCallID         = "unknown"
)

// missingToolResult builds the synthetic error result for an unanswered call.
// The call's name is folded into the text because llm.Message has no dedicated
// field for a tool result's name.
func missingToolResult(call llm.ToolCall) llm.Message {
	name := strings.TrimSpace(call.Function.Name)
	if name == "" {
		name = unknownToolName
	}
	id := strings.TrimSpace(call.ID)
	if id == "" {
		id = unknownCallID
	}
	return llm.Message{
		Role:       "tool",
		ToolCallID: call.ID,
		Content:    fmt.Sprintf("error: %s for tool %q (call %s)", missingToolResultText, name, id),
	}
}

// NormalizeToolPairing repairs a transcript *view* so every assistant tool call
// has a matching tool result before the next user/assistant boundary. Strict
// providers (notably Anthropic) reject a request whose tool_use block is not
// answered by a tool_result, which is what happens after an interruption, a
// cancelled batch, or a resume that crossed batches.
//
// This is an additive, pure view-layer helper: it never mutates its input and
// returns the exact slice it was given when nothing changed. It is deliberately
// not wired into the run loop (the loop already pairs calls via fillUnrun);
// callers should invoke it on messages read from a session, before handing them
// to a provider.
func NormalizeToolPairing(messages []llm.Message) []llm.Message {
	// pending holds the unanswered calls of the current batch; order keeps
	// their original sequence so synthetic results are deterministic.
	pending := make(map[string]*llm.ToolCall)
	var order []string

	// flush answers every still-unanswered call of the current batch. Results
	// must be contiguous with their tool_use, so they are placed immediately
	// before the boundary (or at the end of the transcript).
	flush := func(out *[]llm.Message) bool {
		if len(order) == 0 {
			return false
		}
		emitted := false
		for _, id := range order {
			call, ok := pending[id]
			if !ok {
				continue
			}
			*out = append(*out, missingToolResult(*call))
			emitted = true
		}
		pending = make(map[string]*llm.ToolCall)
		order = order[:0]
		return emitted
	}

	out := make([]llm.Message, 0, len(messages))
	changed := false

	for _, message := range messages {
		if message.Role == "tool" {
			if message.ToolCallID != "" {
				delete(pending, message.ToolCallID)
			}
			normalized := message
			if strings.TrimSpace(normalized.Content) == "" {
				normalized.Content = emptyToolResultText
				changed = true
			}
			out = append(out, normalized)
			continue
		}

		// Any non-tool message is a boundary: answer outstanding calls first.
		if flush(&out) {
			changed = true
		}

		if message.Role == "assistant" {
			for i := range message.ToolCalls {
				call := message.ToolCalls[i]
				if _, exists := pending[call.ID]; !exists {
					order = append(order, call.ID)
				}
				stored := call
				pending[call.ID] = &stored
			}
		}

		out = append(out, message)
	}

	if flush(&out) {
		changed = true
	}

	if !changed {
		return messages
	}
	return out
}
