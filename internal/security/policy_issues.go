package security

import (
	"encoding/json"
	"fmt"
	"strings"
)

// IssueKindInvalidDecision marks a rule that was skipped because its decision
// was missing or was not a recognized verdict.
const IssueKindInvalidDecision = "invalid_decision"

// PolicyIssue describes one problem found while parsing a policy. A malformed
// rule is skipped — an unknown verdict is never executed — but recording the
// skip keeps a mistyped deny from silently widening the effective policy with
// no observable signal.
type PolicyIssue struct {
	// Kind is a stable machine-readable category, e.g. IssueKindInvalidDecision.
	Kind string
	// Index is the zero-based position of the offending rule in the rules array.
	Index int
	// Raw is the original JSON of the offending rule, for operator diagnosis.
	Raw string
	// Reason is a human-readable explanation of the problem.
	Reason string
}

// String renders one issue on a single line.
func (i PolicyIssue) String() string {
	return fmt.Sprintf("security: rules[%d]: %s: %s (rule %s)", i.Index, i.Kind, i.Reason, i.Raw)
}

// FormatPolicyIssues renders issues one per line, or an empty string when
// there are none. It lets callers surface skipped rules in a log or startup
// warning without changing the PolicyFromJSON signature.
func FormatPolicyIssues(issues []PolicyIssue) string {
	if len(issues) == 0 {
		return ""
	}
	var b strings.Builder
	for i, issue := range issues {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(issue.String())
	}
	return b.String()
}

// ParsePolicyWithIssues parses the `permissions` block of a settings file and
// returns, alongside the policy, every rule that had to be skipped. The JSON
// shape and field handling are identical to PolicyFromJSON; the only addition
// is the diagnostic slice. Callers that ignore the slice keep the original
// behaviour exactly.
//
// A rule with an unknown or missing decision is not executed (fail-closed: an
// unrecognized verdict never grants or denies anything). It is reported as an
// IssueKindInvalidDecision issue so a typo such as "denyy" is no longer
// silently dropped.
func ParsePolicyWithIssues(root string, data []byte) (*Policy, []PolicyIssue, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("security: parsing policy: %w", err)
	}
	var cfg PolicyConfig
	var err error
	fields := []struct {
		camel string
		snake string
		into  *[]string
	}{
		{"denyPaths", "deny_paths", &cfg.DenyPaths},
		{"allowPaths", "allow_paths", &cfg.AllowPaths},
		{"denyCommands", "deny_commands", &cfg.DenyCommands},
		{"denyCommandPaths", "deny_command_paths", &cfg.DenyCommandPaths},
		{"denyTools", "deny_tools", &cfg.DenyTools},
		{"allowTools", "allow_tools", &cfg.AllowTools},
		{"readOnlyTools", "read_only_tools", &cfg.ReadOnlyTools},
		{"mutatingTools", "mutating_tools", &cfg.MutatingTools},
		{"writeRoots", "write_roots", &cfg.WriteRoots},
	}
	for _, field := range fields {
		value, ok := raw[field.camel]
		if !ok {
			value, ok = raw[field.snake]
		}
		if ok {
			if *field.into, err = stringList(value); err != nil {
				return nil, nil, fmt.Errorf("security: policy field %q: %w", field.camel, err)
			}
		}
	}
	var issues []PolicyIssue
	if value, ok := raw["rules"]; ok {
		var ruleIssues []PolicyIssue
		if cfg.Rules, ruleIssues, err = parseRulesWithIssues(value); err != nil {
			return nil, nil, fmt.Errorf("security: policy rules: %w", err)
		}
		issues = append(issues, ruleIssues...)
	}
	return NewPolicy(root, cfg), issues, nil
}

// parseRulesWithIssues decodes the rules array while preserving each element's
// original bytes, so a skipped rule can be reported verbatim. A structurally
// malformed element still fails the whole parse, matching the previous
// behaviour of unmarshalling the array in one step.
func parseRulesWithIssues(raw json.RawMessage) ([]Rule, []PolicyIssue, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, nil, err
	}
	rules := make([]Rule, 0, len(items))
	var issues []PolicyIssue
	for index, item := range items {
		var parsed struct {
			Decision string `json:"decision"`
			Tool     string `json:"tool"`
			Path     string `json:"path"`
			Command  string `json:"command"`
		}
		if err := json.Unmarshal(item, &parsed); err != nil {
			return nil, nil, err
		}
		decision, ok := ParseDecision(parsed.Decision)
		if !ok {
			issues = append(issues, PolicyIssue{
				Kind:   IssueKindInvalidDecision,
				Index:  index,
				Raw:    string(item),
				Reason: fmt.Sprintf("invalid decision %q (expected allow, ask, or deny); rule skipped", parsed.Decision),
			})
			continue
		}
		rules = append(rules, Rule{
			Decision: decision,
			Tool:     strings.TrimSpace(parsed.Tool),
			Path:     strings.TrimSpace(parsed.Path),
			Command:  strings.TrimSpace(parsed.Command),
		})
	}
	return rules, issues, nil
}
