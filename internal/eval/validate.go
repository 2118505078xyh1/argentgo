package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidScript marks a scenario whose scripted provider cannot be replayed
// as written. Callers can classify load-time failures with errors.Is.
var ErrInvalidScript = errors.New("invalid scripted step")

// Validate checks the scenario's scripted model turns before a run starts.
// Parse (and therefore Load) calls it, so a typo in a hand-written document is
// reported with the scenario name and turn number instead of surfacing later
// as a confusing tool error or a desynchronised script.
func (s Scenario) Validate() error {
	name := strings.TrimSpace(s.Name)
	if name == "" {
		name = "unnamed"
	}
	for i, turn := range s.Provider {
		if err := validateTurn(turn); err != nil {
			return fmt.Errorf("scenario %q: turn %d: %w", name, i+1, err)
		}
	}
	return nil
}

// validateTurn checks one ScriptTurn. A turn is invalid when it cannot produce
// a usable model reply, or when its two cache expectations contradict each
// other and can never both hold.
func validateTurn(turn ScriptTurn) error {
	if turn.ExpectRequestCached && turn.ExpectRequestCacheFree {
		return fmt.Errorf("%w: expect_request_cached and expect_request_cache_free are mutually exclusive", ErrInvalidScript)
	}
	for i, call := range turn.Respond.ToolCalls {
		tool := strings.TrimSpace(call.Name)
		if tool == "" {
			return fmt.Errorf("%w: tool call %d has an empty name", ErrInvalidScript, i+1)
		}
		// ArgumentsString already unwraps a JSON string holding JSON and
		// defaults a missing value to {}. Everything else must be an object:
		// the agent's tool runner decodes arguments into a struct.
		var args map[string]json.RawMessage
		if err := json.Unmarshal([]byte(call.ArgumentsString()), &args); err != nil {
			return fmt.Errorf("%w: tool call %d (%s): arguments are not a JSON object: %v",
				ErrInvalidScript, i+1, tool, err)
		}
	}
	return nil
}
