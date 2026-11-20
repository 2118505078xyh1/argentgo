// Package workflow runs scripted multi-step tasks.
//
// A definition is a small JSON document:
//
//	{"name":"review","steps":[
//	  {"name":"scan","prompt":"list the changed files"},
//	  {"name":"review","prompt":"review {{steps.scan.output}} for {{input}}"},
//	  {"name":"report","prompt":"write the report","when":{"previous_contains":"BUG"}}
//	]}
//
// Steps run in dependency order: a step that reads {{steps.NAME.output}} runs
// after the step that produces it. Each step either prompts the model or calls
// a tool, and can carry a condition on the previous step's output, a timeout,
// retries with exponential backoff, and an on_failure policy.
package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// Failure policies.
const (
	// OnFailureStop ends the run; the remaining steps are recorded as skipped.
	OnFailureStop = "stop"
	// OnFailureContinue keeps going with the next step.
	OnFailureContinue = "continue"
	// OnFailureRetry retries the step (at least once) and stops when the
	// attempts are exhausted.
	OnFailureRetry = "retry"
)

// Duration is a JSON-friendly time.Duration: it marshals as a string ("30s")
// and accepts a duration string or a number of seconds when decoding.
type Duration time.Duration

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String renders the duration the way time.Duration does.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON writes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts "45s", 90 (seconds), "" and null.
func (d *Duration) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" {
		*d = 0
		return nil
	}
	if strings.HasPrefix(text, `"`) {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		if strings.TrimSpace(s) == "" {
			*d = 0
			return nil
		}
		v, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("workflow: duration %q: %w", s, err)
		}
		*d = Duration(v)
		return nil
	}
	var seconds float64
	if err := json.Unmarshal(data, &seconds); err != nil {
		return fmt.Errorf("workflow: duration must be a string like \"30s\" or a number of seconds: %w", err)
	}
	*d = Duration(time.Duration(seconds * float64(time.Second)))
	return nil
}

// When gates a step on the output of the previous step.
type When struct {
	// PreviousContains runs the step only if the previous output contains it.
	PreviousContains string `json:"previous_contains,omitempty"`
	// PreviousNotContains runs the step only if the previous output does not
	// contain it.
	PreviousNotContains string `json:"previous_not_contains,omitempty"`
}

// matches reports whether the condition holds. prev is the output of the last
// step that actually ran; a skipped step never becomes the previous.
func (w When) matches(prev string, havePrev bool) bool {
	if !havePrev {
		return w.PreviousContains == "" && w.PreviousNotContains == ""
	}
	if w.PreviousContains != "" && !strings.Contains(prev, w.PreviousContains) {
		return false
	}
	if w.PreviousNotContains != "" && strings.Contains(prev, w.PreviousNotContains) {
		return false
	}
	return true
}

// StepCheck is an objective shell check for a step. A zero exit code means the
// step reached its goal; anything else (including a timeout) is a step failure
// that goes through the normal retry path.
type StepCheck struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// Step is one unit of work.
type Step struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt,omitempty"`
	// Tool names a tool to call instead of prompting the model.
	Tool string `json:"tool,omitempty"`
	// Args is the tool argument JSON; it supports the same templates as Prompt.
	Args string `json:"args,omitempty"`
	When When   `json:"when,omitzero"`
	// Retries is the number of extra attempts after a failure.
	Retries int `json:"retries,omitempty"`
	// Timeout bounds one attempt.
	Timeout   Duration `json:"timeout,omitzero"`
	OnFailure string   `json:"on_failure,omitempty"`
	// SuccessCheck, when set, must pass after the primary action succeeds.
	SuccessCheck *StepCheck `json:"success_check,omitempty"`
	// CleanupCommand is a shell command run once when the step has failed, just
	// before the on_failure policy is applied. Its output is appended to the
	// failure diagnostic and its exit code never changes the decision. It is a
	// separate field because OnFailure already names the failure policy.
	CleanupCommand string `json:"cleanup,omitempty"`
}

// WorkflowParam declares one template parameter. Its value is supplied at run
// time (or taken from Default) and is referenced as {{key}} in prompts and
// tool args.
type WorkflowParam struct {
	Key         string   `json:"key"`
	InputType   string   `json:"input_type"`
	Requirement string   `json:"requirement"`
	Description string   `json:"description,omitempty"`
	Default     any      `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// Parameter input types and requirements.
const (
	ParamTypeString  = "string"
	ParamTypeNumber  = "number"
	ParamTypeBoolean = "boolean"
	ParamTypeDate    = "date"
	ParamTypeFile    = "file"
	ParamTypeSelect  = "select"

	ParamRequired   = "required"
	ParamOptional   = "optional"
	ParamUserPrompt = "user_prompt"
)

// Definition is a parsed workflow document.
type Definition struct {
	Name  string `json:"name"`
	Steps []Step `json:"steps"`
	// Parameters declares the template variables a workflow accepts.
	Parameters []WorkflowParam `json:"parameters,omitempty"`
	// Warnings reports invisible characters found in the declarative content.
	// It is metadata only: it is never serialised and never blocks a run.
	Warnings []string `json:"-"`
}

// MaxRetries caps the retry budget so a typo cannot spin forever.
const MaxRetries = 10

// ParseDefinition decodes and validates a workflow document.
func ParseDefinition(data []byte) (*Definition, error) {
	var def Definition
	if err := json.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("workflow: invalid document: %w", err)
	}
	def.normalise()
	if err := def.Validate(); err != nil {
		return nil, err
	}
	def.Warnings = security.ScanArtifact(string(data)).Warnings()
	return &def, nil
}

// LoadDefinition reads a workflow document from disk.
func LoadDefinition(path string) (*Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("workflow: read %s: %w", path, err)
	}
	def, err := ParseDefinition(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return def, nil
}

func (d *Definition) normalise() {
	if strings.TrimSpace(d.Name) == "" {
		d.Name = "workflow"
	}
	for i := range d.Steps {
		step := &d.Steps[i]
		if strings.TrimSpace(step.OnFailure) == "" {
			step.OnFailure = OnFailureStop
		}
		step.OnFailure = strings.ToLower(strings.TrimSpace(step.OnFailure))
		if step.Retries < 0 {
			step.Retries = 0
		}
		if step.Retries > MaxRetries {
			step.Retries = MaxRetries
		}
	}
}

// Validate checks names, actions, conditions and policies.
func (d *Definition) Validate() error {
	if d == nil {
		return fmt.Errorf("workflow: the definition is nil")
	}
	if len(d.Steps) == 0 {
		return fmt.Errorf("workflow: %q has no steps", d.Name)
	}
	seen := make(map[string]bool, len(d.Steps))
	for i, step := range d.Steps {
		name := strings.TrimSpace(step.Name)
		if name == "" {
			return fmt.Errorf("workflow: step %d needs a name", i+1)
		}
		if seen[name] {
			return fmt.Errorf("workflow: duplicate step name %q", name)
		}
		seen[name] = true
		if strings.TrimSpace(step.Prompt) == "" && strings.TrimSpace(step.Tool) == "" {
			return fmt.Errorf("workflow: step %q needs a prompt or a tool", name)
		}
		switch step.OnFailure {
		case "", OnFailureStop, OnFailureContinue, OnFailureRetry:
		default:
			return fmt.Errorf("workflow: step %q has an unknown on_failure %q (use stop, continue or retry)", name, step.OnFailure)
		}
		if step.Timeout.Std() < 0 {
			return fmt.Errorf("workflow: step %q has a negative timeout", name)
		}
	}
	// Step-output references are dependencies. Reject a broken graph here so
	// a definition cannot reach the executor with a cycle or a dangling
	// {{steps.NAME.output}} reference.
	if _, err := ValidateDAG(d.Steps); err != nil {
		return err
	}
	if err := ValidateParameters(*d); err != nil {
		return err
	}
	// A default whose value contradicts its declared input_type would be
	// stringified into a prompt or tool arg at run time; reject it here so a
	// definition cannot reach the executor with a malformed default.
	if err := ValidateParamDefaults(*d); err != nil {
		return err
	}
	return nil
}
