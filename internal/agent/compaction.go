package agent

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// Failure is one tool or provider failure that happened during a run.
type Failure struct {
	Turn   int
	Tool   string
	Reason string
	Error  bool // true for a provider-level error event
}

// CompactionEvidence is the runtime-side information a compactor must keep.
// A session compactor should carry Failures and NextSteps into its summary:
// dropping them loses exactly the context a resumed run needs most.
type CompactionEvidence struct {
	Failures  []Failure
	NextSteps []string
}

const (
	evidenceMaxSteps  = 20
	evidenceStepRunes = 240
)

var nextStepPrefixes = []string{
	"next:", "next step", "next steps", "todo:", "follow up:", "follow-up:",
	"remaining:", "下一步", "后续",
}

// CollectCompactionEvidence extracts failure records and explicit next-step
// lines from one run's events and model-visible messages.
func CollectCompactionEvidence(events []Event, messages []llm.Message) CompactionEvidence {
	out := CompactionEvidence{}
	for _, event := range events {
		switch {
		case event.Kind == EventToolEnd && event.IsError:
			out.Failures = append(out.Failures, Failure{
				Turn:   event.Turn,
				Tool:   event.ToolName,
				Reason: trimEvidence(event.Output),
			})
		case event.Kind == EventError:
			out.Failures = append(out.Failures, Failure{
				Turn:   event.Turn,
				Reason: trimEvidence(event.Err),
				Error:  true,
			})
		}
	}
	seen := make(map[string]bool)
	for _, message := range messages {
		if message.Role != "assistant" || message.Content == "" {
			continue
		}
		for _, line := range strings.Split(message.Content, "\n") {
			line = strings.TrimSpace(line)
			if !isNextStep(line) {
				continue
			}
			line = trimEvidence(line)
			if line == "" || seen[line] {
				continue
			}
			seen[line] = true
			out.NextSteps = append(out.NextSteps, line)
			if len(out.NextSteps) >= evidenceMaxSteps {
				return out
			}
		}
	}
	return out
}

// isNextStep recognises the short explicit markers a model uses to record
// unfinished work. It is deliberately narrow: ordinary prose is not a plan.
func isNextStep(line string) bool {
	lowered := strings.ToLower(line)
	for _, prefix := range nextStepPrefixes {
		if strings.HasPrefix(lowered, prefix) {
			return true
		}
	}
	return false
}

// trimEvidence keeps a readable, rune-safe line for a compaction summary.
func trimEvidence(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= evidenceStepRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:evidenceStepRunes]) + "..."
}

// SortFailuresByTurn returns failures ordered by turn then tool, so a summary
// written from different event sinks is byte-stable.
func SortFailuresByTurn(failures []Failure) []Failure {
	out := append([]Failure(nil), failures...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Turn != out[j].Turn {
			return out[i].Turn < out[j].Turn
		}
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}
