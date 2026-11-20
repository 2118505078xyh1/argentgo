package workflow

import (
	"fmt"
	"strings"
)

// Step-output references are delimited by these around the referenced step
// name, e.g. {{steps.scan.output}} means "this step reads scan's output".
const (
	stepOutputPrefix = "steps."
	stepOutputSuffix = ".output"
)

// referencedSteps returns the names of the steps whose output one step reads
// through {{steps.NAME.output}} placeholders in its prompt and args. These are
// the only two template fields the runner substitutes (see Executor.attempt),
// so they are the only places a step can depend on another step. Names are
// deduplicated and malformed references with an empty NAME are ignored.
func referencedSteps(step Step) []string {
	seen := make(map[string]bool)
	var names []string
	for _, field := range []string{step.Prompt, step.Args} {
		for _, ph := range ExtractPlaceholders(field) {
			if !strings.HasPrefix(ph, stepOutputPrefix) || !strings.HasSuffix(ph, stepOutputSuffix) {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(ph, stepOutputPrefix), stepOutputSuffix)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// ValidateDAG checks that the step-output dependencies form a directed acyclic
// graph and returns the step indices in an order where each step's
// dependencies come before it.
//
// The graph is implicit: step i depends on step j when i's prompt or args
// contain {{steps.<name of j>.output}}. This promotes the template syntax the
// runner already resolves into an executable graph, so a broken definition can
// be rejected before any model or tool is invoked. Errors name the offending
// step so a workflow can be shared and the message pasted into an issue;
// declared values are never echoed.
//
// The returned order is deterministic: among all steps that are ready it
// always picks the smallest declaration index. A definition with no step
// references yields the declaration order unchanged.
func ValidateDAG(steps []Step) (order []int, err error) {
	n := len(steps)
	if n == 0 {
		return nil, nil
	}

	// Map step names to indices. Validate() has already guaranteed unique,
	// non-empty names by the time this is called from there; for a direct call
	// the first declaration wins so the function stays total and deterministic.
	index := make(map[string]int, n)
	for i, step := range steps {
		name := strings.TrimSpace(step.Name)
		if _, exists := index[name]; !exists {
			index[name] = i
		}
	}

	deps := make([][]int, n)       // steps that each step depends on
	indegree := make([]int, n)     // number of unmet dependencies
	dependents := make([][]int, n) // reverse edges, for the topological pass
	for i, step := range steps {
		local := make(map[int]bool)
		for _, ref := range referencedSteps(step) {
			j, ok := index[ref]
			if !ok {
				return nil, fmt.Errorf("workflow: step %q depends on undefined step %q", strings.TrimSpace(step.Name), ref)
			}
			// A self-reference (j == i) is recorded as an edge to itself and
			// is reported as a cycle by the topological pass below.
			if !local[j] {
				local[j] = true
				deps[i] = append(deps[i], j)
			}
		}
		indegree[i] = len(deps[i])
		for _, j := range deps[i] {
			dependents[j] = append(dependents[j], i)
		}
	}

	ready := make([]bool, n)
	done := make([]bool, n)
	for i := range ready {
		ready[i] = indegree[i] == 0
	}
	order = make([]int, 0, n)
	for len(order) < n {
		picked := -1
		for i := range done {
			if ready[i] && !done[i] {
				picked = i
				break
			}
		}
		if picked < 0 {
			// No ready step remains, so the not-yet-processed steps form at
			// least one cycle (possibly with steps that depend on that cycle).
			stuck := make([]string, 0, n-len(order))
			for i := range done {
				if !done[i] {
					stuck = append(stuck, fmt.Sprintf("%q", strings.TrimSpace(steps[i].Name)))
				}
			}
			return nil, fmt.Errorf("workflow: cyclic step dependency among %s", strings.Join(stuck, ", "))
		}
		done[picked] = true
		order = append(order, picked)
		for _, d := range dependents[picked] {
			indegree[d]--
			if indegree[d] == 0 {
				ready[d] = true
			}
		}
	}
	return order, nil
}
