package agent

import (
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

const (
	// charsPerToken is the standard rule-of-thumb ratio for English and code.
	charsPerToken = 4
	// messageOverheadTokens covers the role marker and message framing every
	// provider adds to each entry.
	messageOverheadTokens = 4
	// trimmedPrefix marks a tool result whose body was reclaimed. One line
	// stays so the model still knows the call happened.
	trimmedPrefix = "[tool output trimmed:"
	// supersededStub replaces an old read that a newer read of the same file
	// made redundant. Keeping a stub preserves the tool call/result pairing.
	supersededStub = "[Superseded by a newer read of this file]"
	// uselessStub replaces an empty search result once a later assistant turn
	// has consumed it.
	uselessStub = "[Uneventful result elided]"
	// cacheProtectSuffixTokens is the conservative prompt-cache guard: when a
	// result still has more than this many estimated tokens after it, stubbing
	// it would invalidate a large cached prefix, so leave it alone.
	cacheProtectSuffixTokens = 12000

	// toolResultPruneThreshold is the rune count above which a tool result's
	// middle is replaced with a marker instead of keeping the whole body.
	toolResultPruneThreshold = 8192
	// toolResultPruneHead and toolResultPruneTail are the number of leading and
	// trailing runes retained when pruning an over-budget tool result. They
	// mirror the dsh compaction-tool-result-pruner defaults.
	toolResultPruneHead = 4096
	toolResultPruneTail = 1024
	// toolResultPruneMarker substitutes the removed middle span. The surrounding
	// blank lines keep the retained head and tail visually separated from it.
	toolResultPruneMarker = "\n\n[... tool result middle pruned ...]\n\n"
)

// EstimateTokens estimates the token cost of a string.
//
// Wide (CJK) runes cost roughly one token each in every provider tokenizer,
// whereas the charsPerToken rule applies to Latin text and code. Counting them
// separately avoids the severe under-estimate that chars/4 alone gives CJK,
// while still over-estimating relative to the most efficient tokenizers (the
// safe direction for a budget).
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	wide, narrow := 0, 0
	for _, r := range text {
		if isWideRune(r) {
			wide++
		} else {
			narrow++
		}
	}
	return wide + (narrow+charsPerToken-1)/charsPerToken
}

// isWideRune reports whether r belongs to a script that tokenizers treat as
// roughly one token per rune: Han, Kana, Hangul, fullwidth forms and CJK
// punctuation.
func isWideRune(r rune) bool {
	// Han, Kana, Hangul, CJK punctuation, fullwidth forms, common emoji and the
	// CJK extension planes. Grouped as ranges to keep the hot path allocation-free.
	return (r >= 0x1100 && r <= 0x115F) || // Hangul Jamo
		(r >= 0x2E80 && r <= 0xA4CF) || // CJK radicals, Kangxi, Han
		(r >= 0xAC00 && r <= 0xD7A3) || // Hangul syllables
		(r >= 0xF900 && r <= 0xFAFF) || // CJK compatibility ideographs
		(r >= 0xFE10 && r <= 0xFE19) || // CJK punctuation
		(r >= 0xFE30 && r <= 0xFE6F) || // CJK compatibility forms
		(r >= 0xFF00 && r <= 0xFF60) || // fullwidth forms
		(r >= 0xFFE0 && r <= 0xFFE6) ||
		(r >= 0x1F300 && r <= 0x1FAFF) || // symbols/emoji are commonly 1+ tokens
		(r >= 0x20000 && r <= 0x3FFFD) // CJK extension planes
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
	// ToolMeta maps ToolCallID to the metadata produced by the tool runner.
	// When non-empty, superseded reads and useless results are reclaimed first.
	ToolMeta map[string]ToolMeta
	// CacheProtectSuffixTokens overrides the prompt-cache guard. Zero uses the
	// conservative default.
	CacheProtectSuffixTokens int
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
	// Meta-based reclamation is cheap (no model call) and independent of the
	// token budget, so it runs first even when a caller disabled size trimming.
	if len(opts.ToolMeta) > 0 {
		reclaimByMeta(out, opts, &res)
	}
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

	effectiveProtect := effectiveCacheProtect(opts)
	protected := protectedIndexes(out, keepTurns)
	recentTool := keepToolTokens
	for i := len(out) - 1; i >= 0; i-- {
		msg := out[i]
		if msg.Role != "tool" || protected[i] {
			continue
		}
		// The budget loop walks backwards, so the suffix only grows as i moves
		// earlier. Once stubbing would rewrite a byte range that still has more
		// than protect tokens cached after it, every earlier position is equally
		// unsafe: stop instead of invalidating the warm prefix and triggering a
		// full re-prefill. This mirrors the guard in reclaimByMeta.
		if suffixTokens(out, i) > effectiveProtect {
			break
		}
		cost := EstimateMessage(msg)
		if recentTool > 0 {
			recentTool -= cost
			continue
		}
		if msg.Content == "" || strings.HasPrefix(msg.Content, trimmedPrefix) {
			continue
		}
		stub := toolStub(msg.Content)
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

// pruneToolResultMiddle retains the head and tail of an over-budget tool result
// and replaces the removed middle with toolResultPruneMarker.
//
// Slicing is by rune, never by byte, so a multi-byte UTF-8 character (CJK, emoji)
// that straddles a budget boundary is kept intact instead of being cut in half.
// A result at or below toolResultPruneThreshold is returned unchanged, which
// makes the function safe to call unconditionally.
func pruneToolResultMiddle(content string) string {
	runes := []rune(content)
	if len(runes) <= toolResultPruneThreshold {
		return content
	}
	// Keep the replacement strictly smaller than the original. The fixed
	// threshold/budget relationship already guarantees this, but guard it so a
	// future budget change cannot turn a "prune" into growth.
	if toolResultPruneHead+toolResultPruneTail >= len(runes) {
		return content
	}
	var b strings.Builder
	b.Grow(len(content) + len(toolResultPruneMarker))
	b.WriteString(string(runes[:toolResultPruneHead]))
	b.WriteString(toolResultPruneMarker)
	b.WriteString(string(runes[len(runes)-toolResultPruneTail:]))
	return b.String()
}

// toolStub builds the replacement for a pruned tool result. The trimmedPrefix
// header stays first so every stub-detection site keeps recognising it. For an
// over-budget body the head and tail are preserved around a marker so the model
// retains some detail; smaller bodies fall back to the compact one-line stub so
// the budget loop still reclaims space.
func toolStub(content string) string {
	pruned := pruneToolResultMiddle(content)
	removed := len([]rune(content)) - len([]rune(pruned))
	header := fmt.Sprintf("%s %d chars of this tool result were removed to save context; run the tool again if the details matter]", trimmedPrefix, removed)
	if pruned == content {
		return header
	}
	return header + "\n" + pruned
}

// reclaimByMeta stamps tool results that later turns made redundant. It never
// removes a message: a compact stub always takes the old body's place, so the
// assistant call and its result stay paired.
//
// Two reclaims happen here:
//   - a read whose key reappears in a later, successful read is superseded;
//   - a useless (empty) result is elided once any later assistant turn exists,
//     i.e. the model has had a chance to consume it.
//
// A result with a large suffix is skipped: stubbing it would invalidate a
// long prompt-cache prefix for a small saving.
func reclaimByMeta(messages []llm.Message, opts TrimOptions, res *TrimResult) {
	protect := effectiveCacheProtect(opts)

	// One reverse pass precomputes the two tail queries the old loop recomputed
	// from scratch for every tool message. suffixToken[i] equals the estimate of
	// everything after i; nextAssistantAfter[i] records whether an assistant
	// turn follows i. The rewriting loop below walks forwards and only ever
	// touches the message at its own index, so every index it queries (> i) is
	// still pristine at that point — the precomputed answers are identical to
	// the old on-demand scans, but O(1) instead of O(n) each.
	suffixToken := make([]int, len(messages))
	nextAssistantAfter := make([]bool, len(messages))
	runningTokens := 0
	sawAssistant := false
	for i := len(messages) - 1; i >= 0; i-- {
		suffixToken[i] = runningTokens
		nextAssistantAfter[i] = sawAssistant
		runningTokens += EstimateMessage(messages[i])
		if messages[i].Role == "assistant" {
			sawAssistant = true
		}
	}

	// newestSuccessful[key] is the last result carrying the key that was not
	// an error. Only a later successful read supersedes an older one: an error
	// must never erase the content the model might still need.
	newestSuccessful := make(map[string]int)
	for i, msg := range messages {
		if msg.Role != "tool" || isErrorResult(msg.Content) {
			continue
		}
		if key := opts.ToolMeta[msg.ToolCallID].SupersedeKey; key != "" {
			newestSuccessful[key] = i
		}
	}

	for i, msg := range messages {
		if msg.Role != "tool" || isErrorResult(msg.Content) {
			continue
		}
		if msg.Content == supersededStub || msg.Content == uselessStub || strings.HasPrefix(msg.Content, trimmedPrefix) {
			continue // already reclaimed on an earlier turn
		}
		meta := opts.ToolMeta[msg.ToolCallID]
		if meta == (ToolMeta{}) {
			continue
		}
		stub := ""
		if meta.SupersedeKey != "" {
			if later, ok := newestSuccessful[meta.SupersedeKey]; ok && later > i {
				stub = supersededStub
			}
		}
		if stub == "" && meta.Useless && nextAssistantAfter[i] {
			stub = uselessStub
		}
		if stub == "" {
			continue
		}
		if suffixToken[i] > protect {
			continue
		}
		replacement := msg
		replacement.Content = stub
		before := EstimateMessage(msg)
		after := EstimateMessage(replacement)
		// Meta reclamation is semantic: an empty search result is replaced even
		// when the stub is a few tokens longer, because the point is to stop the
		// model re-reading a result that carried no information.
		messages[i] = replacement
		res.Pruned++
		if saved := before - after; saved > 0 {
			res.Freed += saved
		}
	}
}

// isErrorResult recognises the error results the loop writes for failed tools.
// Error text is never a candidate for reclamation.
func isErrorResult(content string) bool {
	return strings.HasPrefix(content, "error:")
}

// suffixTokens estimates the size of the conversation that follows index i.
// It is used by the budget loop in Trim, which mutates later entries as it
// walks backwards and therefore cannot reuse the table built by
// reclaimByMeta; that semantic path gets its suffix from the same reverse
// pass as nextAssistantAfter instead.
func suffixTokens(messages []llm.Message, i int) int {
	total := 0
	for j := i + 1; j < len(messages); j++ {
		total += EstimateMessage(messages[j])
	}
	return total
}

// effectiveCacheProtect returns the prompt-cache guard threshold. Both the
// semantic (reclaimByMeta) and budget (Trim) paths use it so a result with a
// large cached suffix is never rewritten by either one.
func effectiveCacheProtect(opts TrimOptions) int {
	if opts.CacheProtectSuffixTokens > 0 {
		return opts.CacheProtectSuffixTokens
	}
	return cacheProtectSuffixTokens
}
