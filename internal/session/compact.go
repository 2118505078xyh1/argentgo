package session

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Compaction is the payload of a kind:"compaction" record. The summary
// replaces the dropped part of the conversation; Keep is how many of the most
// recent messages still follow it verbatim.
type Compaction struct {
	Summary   string   `json:"summary"`
	Dropped   int      `json:"dropped"`
	Keep      int      `json:"keep"`
	Files     []string `json:"files,omitempty"`
	Failures  []string `json:"failures,omitempty"`
	NextSteps []string `json:"nextSteps,omitempty"`
}

// Compact replaces the older part of the conversation with one summary record
// and keeps the last keep messages. It is a no-op when the history already fits.
func (s *Session) Compact(keep int) error {
	if keep < 0 {
		return fmt.Errorf("session: keep must be >= 0, got %d", keep)
	}
	entries := s.Messages()
	if len(entries) <= keep {
		return nil
	}
	start := len(entries) - keep
	// A tool result never arrives without the assistant message that called it.
	for start > 0 && start < len(entries) && entries[start].Role == "tool" {
		start--
	}
	if start == 0 {
		return nil
	}
	compaction := Summarize(entries[:start], len(entries)-start)
	return s.Append(KindCompaction, compaction)
}

// Summarize turns the dropped entries into a compaction payload that keeps the
// structure a later turn needs: goal, touched files, failures and next steps.
func Summarize(dropped []Entry, keep int) Compaction {
	var goals, files, failures, next []string
	seenFiles := make(map[string]bool)

	for _, entry := range dropped {
		switch entry.Role {
		case "user":
			if goal := firstLine(entry.Content, 200); goal != "" {
				goals = append(goals, goal)
			}
		case "tool":
			if reason, ok := failureReason(entry.Content); ok {
				failures = append(failures, failureBullet(entry.Name, reason))
			}
		}
		if entry.Role == "assistant" || entry.Role == "system" {
			lower := strings.ToLower(entry.Content)
			if strings.Contains(lower, "next step") ||
				strings.Contains(lower, "next:") ||
				strings.Contains(lower, "todo") {
				if step := firstLine(entry.Content, 200); step != "" {
					next = append(next, step)
				}
			}
		}
		for _, path := range pathsFromData(entry.Data) {
			if !seenFiles[path] {
				seenFiles[path] = true
				files = append(files, path)
			}
		}
	}
	sort.Strings(files)
	goals = tailItems(goals, 5)
	files = tailItems(files, 12)
	failures = tailItems(failures, 8)
	next = tailItems(next, 5)

	var summary strings.Builder
	summary.WriteString("## Compaction\n")
	fmt.Fprintf(&summary, "- dropped %d message(s), kept the last %d\n", len(dropped), keep)
	writeSection(&summary, "Goal", goals)
	writeSection(&summary, "Files changed", files)
	writeSection(&summary, "Failures", failures)
	writeSection(&summary, "Next steps", next)

	return Compaction{
		Summary:   summary.String(),
		Dropped:   len(dropped),
		Keep:      keep,
		Files:     files,
		Failures:  failures,
		NextSteps: next,
	}
}

func writeSection(builder *strings.Builder, title string, items []string) {
	fmt.Fprintf(builder, "\n## %s\n", title)
	if len(items) == 0 {
		builder.WriteString("- (none)\n")
		return
	}
	for _, item := range items {
		fmt.Fprintf(builder, "- %s\n", item)
	}
}

// tailItems keeps the most recent limit entries.
func tailItems(items []string, limit int) []string {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

// failureReason recognizes a tool result that represents a failure.
func failureReason(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	if index := strings.LastIndex(lower, "[exit "); index >= 0 {
		tail := trimmed[index:]
		code := 0
		digits := 0
		rest := strings.TrimPrefix(tail, "[exit ")
		for _, ch := range rest {
			if ch < '0' || ch > '9' {
				break
			}
			code = code*10 + int(ch-'0')
			digits++
		}
		if digits > 0 && code != 0 {
			return firstLine(tail, 160), true
		}
	}
	if strings.Contains(lower, "timed out") {
		return "timed out", true
	}
	first := firstLine(trimmed, 160)
	lowerFirst := strings.ToLower(first)
	if strings.HasPrefix(lowerFirst, "error") ||
		strings.HasPrefix(lowerFirst, "failed") ||
		strings.Contains(lower, "panicked at") ||
		strings.Contains(lower, "traceback (most recent call last)") {
		return first, true
	}
	return "", false
}

func failureBullet(name, reason string) string {
	if strings.TrimSpace(name) == "" {
		name = "tool"
	}
	return name + " — " + reason
}

// pathsFromData collects file paths mentioned by a record payload, including
// tool-call arguments that are themselves JSON strings.
func pathsFromData(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	collectPaths(value, &out, seen)
	return out
}

func collectPaths(value any, out *[]string, seen map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch key {
			case "path", "file":
				if text, ok := child.(string); ok {
					addPath(out, seen, text)
				}
			case "files":
				switch list := child.(type) {
				case []any:
					for _, item := range list {
						if text, ok := item.(string); ok {
							addPath(out, seen, text)
						}
					}
				case string:
					addPath(out, seen, list)
				}
			case "arguments", "args", "input":
				if text, ok := child.(string); ok {
					var inner any
					if json.Unmarshal([]byte(text), &inner) == nil {
						collectPaths(inner, out, seen)
					}
					continue
				}
				collectPaths(child, out, seen)
			default:
				collectPaths(child, out, seen)
			}
		}
	case []any:
		for _, item := range typed {
			collectPaths(item, out, seen)
		}
	}
}

func addPath(out *[]string, seen map[string]bool, path string) {
	path = strings.TrimSpace(path)
	if path == "" || seen[path] {
		return
	}
	seen[path] = true
	*out = append(*out, path)
}
