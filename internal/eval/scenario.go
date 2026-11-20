// Package eval runs deterministic agent scenarios against a scripted model.
//
// A unit test proves that a function returns what it should. It does not prove
// that the agent, its tools and its policy still agree with each other after a
// change. That is what an eval is for: a set of scenarios, each with a scripted
// model, run against the real loop and checked against what actually happened.
//
// The model is scripted, so an eval is deterministic and costs nothing. The
// loop and its tools are real, so a broken tool fails the eval.
//
//	{
//	  "scenarios": [{
//	    "name": "reads a file before answering",
//	    "prompt": "what is in notes.txt?",
//	    "files": {"notes.txt": "the release notes are here\n"},
//	    "provider": [
//	      {"respond": {"tool_calls": [{"name": "read", "arguments": {"path": "notes.txt"}}]}},
//	      {"expect_request_contains": ["release notes"],
//	       "respond": {"text": "notes.txt says the release notes are here"}}
//	    ],
//	    "expect": {"tool_calls": ["read"], "text_contains": ["release notes"], "turns": 2},
//	    "check": {"files_exist": ["notes.txt"], "file_contains": {"notes.txt": "release"}}
//	  }]
//	}
//
// The package does not import the agent: the caller injects a RunFunc, so the
// same scenarios can drive any loop implementation.
package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// ToolCall is a tool invocation the scripted model asks for.
type ToolCall struct {
	Name string `json:"name"`
	// Arguments is raw JSON. A JSON string holding JSON ("{\"path\":\"a\"}")
	// is accepted too, because scripts are written by hand.
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// UnmarshalJSON accepts arguments as either an object or a JSON string.
func (c *ToolCall) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Name = raw.Name
	c.Arguments = raw.Arguments
	if trimmed := bytes.TrimSpace(c.Arguments); len(trimmed) > 0 && trimmed[0] == '"' {
		var inner string
		if err := json.Unmarshal(trimmed, &inner); err == nil {
			c.Arguments = json.RawMessage(inner)
		}
	}
	return nil
}

// ArgumentsString returns the raw arguments, defaulting to an empty object.
func (c ToolCall) ArgumentsString() string {
	trimmed := string(bytes.TrimSpace(c.Arguments))
	if trimmed == "" || trimmed == "null" {
		return "{}"
	}
	return trimmed
}

// Respond is the message the scripted model returns for one turn.
type Respond struct {
	Text      string     `json:"text,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Usage is billed usage for this turn, including any cached_tokens count.
	Usage *llm.Usage `json:"usage,omitempty"`
}

// ScriptTurn is one scripted model reply plus an optional expectation about the
// request that triggered it.
type ScriptTurn struct {
	// ExpectRequestContains lists substrings that must appear in the
	// conversation the model is sent: message text, tool names, tool arguments
	// and tool results. It is how a scenario asserts that a tool result really
	// made it back into the context.
	ExpectRequestContains []string `json:"expect_request_contains,omitempty"`
	// ExpectRequestCacheFree fails when any message in the request carries a
	// stable-prefix cache marker (the prompt-cache opt-out case).
	ExpectRequestCacheFree bool `json:"expect_request_cache_free,omitempty"`
	// ExpectRequestCached fails when no message carries a cache marker.
	ExpectRequestCached bool    `json:"expect_request_cached,omitempty"`
	Respond             Respond `json:"respond"`
}

// Expect is what a scenario expects to have happened.
type Expect struct {
	// ToolCalls is the exact tool sequence, in order. nil means "do not
	// check"; an empty (non-nil) list means "no tool may run".
	ToolCalls []string `json:"tool_calls"`
	// TextContains lists substrings the final answer must contain.
	TextContains []string `json:"text_contains,omitempty"`
	// TextNotContains lists substrings the final answer must not contain.
	TextNotContains []string `json:"text_not_contains,omitempty"`
	// Turns is the expected number of model turns, counted by the harness from
	// the scripted provider, not reported by the loop.
	Turns *int `json:"turns,omitempty"`
	// Stopped says whether the run was stopped by something other than the
	// model finishing: a turn limit, a policy block, a cancellation.
	Stopped *bool `json:"stopped,omitempty"`
	// CachedTokens is the expected total of cached prompt tokens reported by
	// the scripted provider. nil means "do not check".
	CachedTokens *int `json:"cached_tokens,omitempty"`
}

// Check inspects the workspace after the run.
type Check struct {
	// FilesExist lists paths, relative to the workspace, that must exist.
	FilesExist []string `json:"files_exist,omitempty"`
	// FileContains maps a path to a needle its content must contain.
	FileContains map[string]string `json:"file_contains,omitempty"`

	// Exec, when set and AllowExec is true, is run inside the seeded temp
	// workspace after the other checks (see RunExecCheck / GradeExecCheck).
	Exec *ExecCheck `json:"exec,omitempty"`
	// AllowExec is the explicit opt-in for Exec. Command execution is
	// default-off: an Exec without AllowExec is never run, so a scenario
	// document cannot execute a command unless it asks for it by name.
	AllowExec bool `json:"allow_exec,omitempty"`
}

// Scenario is one scripted eval case.
type Scenario struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Model and System are passed to the loop as-is; Model defaults to
	// DefaultModel.
	Model  string `json:"model,omitempty"`
	System string `json:"system,omitempty"`
	Prompt string `json:"prompt"`
	// Provider is the script the fake model replays, one entry per turn.
	Provider []ScriptTurn `json:"provider"`
	Expect   Expect       `json:"expect"`
	// Files seeds the workspace before the run, path → content.
	Files map[string]string `json:"files,omitempty"`
	Check Check             `json:"check"`

	// The fields below are hints for the caller's RunFunc. The harness itself
	// only enforces Repeat (through the scripted provider); the rest describe
	// what the scenario wants the loop to do, so a runner does not have to
	// special-case scenarios by name.
	//
	// MaxTurns is the turn budget for the run (0 = the loop's default).
	MaxTurns int `json:"max_turns,omitempty"`
	// CancelAfter asks the runner to cancel the run after that many turns.
	CancelAfter int `json:"cancel_after,omitempty"`
	// Approve is the answer the approval gate should give (nil = runner's
	// default).
	Approve *bool `json:"approve,omitempty"`
	// DenyPaths lists paths the policy must refuse.
	DenyPaths []string `json:"deny_paths,omitempty"`
	// Repeat plays the script in a loop, so the model never stops on its own
	// and only a limit ends the run.
	Repeat bool `json:"repeat,omitempty"`
	// PromptCache is a runner hint: mark the stable system prefix when the
	// runner assembles a cache-capable request.
	PromptCache bool `json:"prompt_cache,omitempty"`
	// NoPromptCache mirrors --no-prompt-cache: the runner must not emit cache
	// markers, even when the protocol supports them.
	NoPromptCache bool `json:"no_prompt_cache,omitempty"`
}

// DefaultModel is the model id handed to the loop when a scenario is silent.
const DefaultModel = "scripted"

// Load reads a scenario document from disk.
func Load(path string) ([]Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	scenarios, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return scenarios, nil
}

// Parse decodes a scenario document: {"scenarios": [...]} or a bare array.
func Parse(data []byte) ([]Scenario, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	var document struct {
		Scenarios []Scenario `json:"scenarios"`
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(data, &document.Scenarios); err != nil {
			return nil, fmt.Errorf("decode scenarios: %w", err)
		}
	} else if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode scenarios: %w", err)
	}
	if len(document.Scenarios) == 0 {
		return nil, fmt.Errorf("no scenarios in document")
	}
	for i := range document.Scenarios {
		document.Scenarios[i].normalize(i)
	}
	return document.Scenarios, nil
}

// normalize applies the defaults a hand-written document may omit.
func (s *Scenario) normalize(index int) {
	if s.Name == "" {
		s.Name = fmt.Sprintf("scenario %d", index+1)
	}
	if s.Model == "" {
		s.Model = DefaultModel
	}
	if s.MaxTurns < 0 {
		s.MaxTurns = 0
	}
	if s.CancelAfter < 0 {
		s.CancelAfter = 0
	}
}
