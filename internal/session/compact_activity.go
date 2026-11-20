package session

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// This file implements the deterministic, zero-LLM "tool activity" part of a
// compaction summary. When a span of the conversation is dropped, the next turn
// still needs to know which files were read (and which lines), which were
// changed, and which commands were run — otherwise the agent re-reads the same
// files or forgets a change it already made. Unlike the goal / failure / next
// step extraction, nothing here calls a model: it reads the recorded tool calls
// directly from the dropped entries.

const (
	activityCommandLimit = 100 // cline-style truncation for a command line
	activityPathLimit    = 120 // keep a path from eating the whole bullet
	activityBulletLimit  = 200 // hard cap on one rendered activity line
	activityMaxBullets   = 24  // keep the summary bounded regardless of transcript size
)

// activityReadTools are tools whose arguments describe reading a file. The
// recorded arguments carry one of path/file/file_path plus offset/limit or a
// line-centred window (line/before/after), from which a path:start-end range is
// reconstructed.
var activityReadTools = map[string]bool{"read": true, "cat": true}

// activityEditTools are tools that change files. Only the path is recoverable
// from a recorded edit: the tool result is a bare "edited <path>" line with no
// diff, so no reliable line range can be derived without touching the file (a
// pure function must not).
var activityEditTools = map[string]bool{
	"edit": true, "write": true, "multi_edit": true, "patch": true,
}

// activityCommandTools are tools whose arguments carry a shell command.
var activityCommandTools = map[string]bool{
	"bash": true, "shell": true, "sh": true, "powershell": true, "pwsh": true,
}

// activityCall is one tool invocation projected out of a transcript record: a
// name and its decoded arguments. It is built only from map lookups, never from
// map iteration, so extraction is deterministic.
type activityCall struct {
	name string
	args map[string]any
}

// summarizeToolActivity returns a markdown "Tool activity" section built from
// the read/edit/command calls in dropped, or "" when there is no such activity.
// It is a pure function: deterministic, deduplicated and bounded in size.
func summarizeToolActivity(dropped []Entry) string {
	var bullets []string
	seen := make(map[string]bool)
	for _, entry := range dropped {
		for _, call := range callsFromData(entry.Data) {
			bullet, ok := activityBullet(call)
			if !ok || bullet == "" || seen[bullet] {
				continue
			}
			seen[bullet] = true
			bullets = append(bullets, bullet)
		}
	}
	if len(bullets) == 0 {
		return ""
	}
	// Chronological order is kept; when there are too many, the most recent
	// activities survive (the ones a resumed turn is most likely to need).
	bullets = tailItems(bullets, activityMaxBullets)

	var b strings.Builder
	b.WriteString("\n## Tool activity\n")
	for _, bullet := range bullets {
		fmt.Fprintf(&b, "- %s\n", truncateRunes(bullet, activityBulletLimit))
	}
	return b.String()
}

// callsFromData extracts tool invocations from one record's payload. Two
// layouts occur in a session file:
//   - a tool result record with top-level "name" and "arguments";
//   - an assistant record with a "tool_calls" (or "toolCalls") array whose
//     items carry function.name and function.arguments.
func callsFromData(raw json.RawMessage) []activityCall {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}

	var out []activityCall
	for _, key := range []string{"tool_calls", "toolCalls"} {
		list, ok := object[key].([]any)
		if !ok {
			continue
		}
		for _, item := range list {
			call, ok := item.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := call["function"].(map[string]any)
			name := ""
			if fn != nil {
				name, _ = fn["name"].(string)
			}
			if name == "" {
				name, _ = call["name"].(string)
			}
			if name == "" {
				continue
			}
			args := decodedArgs(fn["arguments"])
			if args == nil {
				args = decodedArgs(call["arguments"])
			}
			out = append(out, activityCall{name: name, args: args})
		}
	}

	name, _ := object["name"].(string)
	if name != "" && name != "tool" {
		args := decodedArgs(object["arguments"])
		if args == nil {
			args = decodedArgs(object["args"])
		}
		if args == nil {
			args = decodedArgs(object["input"])
		}
		out = append(out, activityCall{name: name, args: args})
	}
	return out
}

// decodedArgs accepts arguments serialized either as a JSON string (the usual
// case in a tool record) or as an already-decoded JSON object.
func decodedArgs(raw any) map[string]any {
	switch typed := raw.(type) {
	case string:
		var object map[string]any
		if json.Unmarshal([]byte(typed), &object) == nil {
			return object
		}
	case map[string]any:
		return typed
	}
	return nil
}

// activityBullet renders one tool call as a single activity line. Unknown tools
// and calls without the expected argument are ignored.
func activityBullet(call activityCall) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(call.name))
	args := call.args
	switch {
	case activityReadTools[name]:
		path := activityPath(args)
		if path == "" {
			return "", false
		}
		return "read " + activityPathRange(path, args), true
	case activityEditTools[name]:
		path := activityPath(args)
		if path == "" {
			return "", false
		}
		return "edit " + truncateRunes(path, activityPathLimit), true
	case activityCommandTools[name]:
		command := ""
		for _, key := range []string{"command", "cmd"} {
			if text, ok := args[key].(string); ok {
				command = text
				break
			}
		}
		if strings.TrimSpace(command) == "" {
			return "", false
		}
		return "bash " + truncateRunes(command, activityCommandLimit), true
	}
	return "", false
}

// activityPath returns the file a tool call refers to, preferring the keys the
// built-in tools actually use.
func activityPath(args map[string]any) string {
	for _, key := range []string{"path", "file", "file_path"} {
		if text, ok := args[key].(string); ok {
			if text = strings.TrimSpace(text); text != "" {
				return text
			}
		}
	}
	return ""
}

// activityPathRange appends a line range to a read path. Explicit window
// arguments win; otherwise a line selector already embedded in the path
// (":10", ":10:20", "#L10", "#L10-L20") is normalised to "path:start-end".
func activityPathRange(path string, args map[string]any) string {
	line := activityInt(args, "line")
	if line > 0 {
		start := line - activityInt(args, "before")
		if start < 1 {
			start = 1
		}
		end := line + activityInt(args, "after")
		if end < start {
			end = start
		}
		return activityFormatPath(path, start, end)
	}
	offset := activityInt(args, "offset")
	limit := activityInt(args, "limit")
	if offset > 0 || limit > 0 {
		start := offset
		if start < 1 {
			start = 1
		}
		end := start
		if limit > 0 {
			end = start + limit - 1
		}
		if end < start {
			end = start
		}
		return activityFormatPath(path, start, end)
	}
	if base, start, end, ok := splitTrailingRange(path); ok {
		return activityFormatPath(base, start, end)
	}
	return truncateRunes(path, activityPathLimit)
}

func activityFormatPath(path string, start, end int) string {
	return fmt.Sprintf("%s:%d-%d", truncateRunes(path, activityPathLimit), start, end)
}

// activityInt reads an integer argument. encoding/json decodes numbers into
// float64 when unmarshalling into any, which is the common case here.
func activityInt(args map[string]any, key string) int {
	switch typed := args[key].(type) {
	case float64:
		return int(typed)
	case json.Number:
		n, _ := typed.Int64()
		return int(n)
	case int:
		return typed
	}
	return 0
}

// splitTrailingRange recognises a line selector embedded in a path and returns
// the path without the selector plus the inclusive start/end lines. It is
// careful not to mistake "C:\dir" for a selector (a colon must be followed by
// digits) and returns ok=false when no selector is present.
func splitTrailingRange(path string) (base string, start, end int, ok bool) {
	if idx := strings.LastIndex(path, "#"); idx >= 0 {
		base = path[:idx]
		start, end, ok = activityRangeToken(path[idx+1:])
		if !ok {
			return "", 0, 0, false
		}
		return base, start, end, true
	}

	i := len(path)
	var nums []int
	for i > 0 {
		j := i - 1
		if path[j] < '0' || path[j] > '9' {
			break
		}
		k := j
		for k > 0 && path[k-1] >= '0' && path[k-1] <= '9' {
			k--
		}
		if k == 0 || path[k-1] != ':' {
			break
		}
		n, err := strconv.Atoi(path[k : j+1])
		if err != nil {
			break
		}
		nums = append([]int{n}, nums...)
		i = k - 1
	}
	if len(nums) == 0 {
		return "", 0, 0, false
	}
	start, end = nums[0], nums[0]
	if len(nums) > 1 {
		end = nums[len(nums)-1]
	}
	if end < start {
		start, end = end, start
	}
	return path[:i], start, end, true
}

// activityRangeToken parses the part after "#", e.g. "L10", "10-20" or
// "L10-L20", into an inclusive line range.
func activityRangeToken(token string) (start, end int, ok bool) {
	parts := strings.FieldsFunc(token, func(r rune) bool { return r == '-' || r == ':' })
	if len(parts) == 0 {
		return 0, 0, false
	}
	// Each component may carry its own "L" prefix, e.g. "L10-L20".
	partInt := func(part string) (int, bool) {
		part = strings.TrimPrefix(strings.TrimPrefix(part, "L"), "l")
		n, err := strconv.Atoi(part)
		return n, err == nil && n > 0
	}
	start, ok = partInt(parts[0])
	if !ok {
		return 0, 0, false
	}
	end = start
	if len(parts) > 1 {
		if n, ok := partInt(parts[1]); ok {
			end = n
		}
	}
	if end < start {
		start, end = end, start
	}
	return start, end, true
}
