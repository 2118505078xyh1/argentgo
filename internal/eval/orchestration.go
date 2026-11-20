package eval

// End-to-end orchestration evals.
//
// Unlike the core suite (whose fakeRun is a hand-written stand-in for the loop),
// this harness drives the REAL agent loop and the REAL task tool. The only
// injected fakes are the things a real harness always injects: a fake model
// (an llm.Chatter, like scripted.go's Scripted) and a no-op ToolRunner for
// non-task tools. Persona resolution, PARENT-RECHECK verification, the bounded
// retry loop, the background queue / drain and the TaskBoard are all the real
// implementations from internal/agent.
//
// The fake model is *routing* rather than a single global queue: a subagent
// shares its parent's Chatter, so the script must choose replies by participant
// (root / child / verification child). Scripted in scripted.go cannot do that;
// this file reuses its Respond/ToolCall wire types but routes each turn by the
// system prompt of the request. That is what lets a single JSON document
// describe a whole multi-turn delegation, including child turns interleaved
// between the parent's turns.
//
// No network, no real model, no cgo. The background scenario gates the child
// reply on a channel so "ack first, result later" is deterministic.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// Script targets. "verify" is a child as well (it runs a nested loop), but it
// is classified separately because the verify prompt is distinctive.
const (
	orchTargetParent = "parent"
	orchTargetChild  = "child"
	orchTargetVerify = "verify"

	// orchRootSystem is the root request's system prompt. Any request whose
	// system message differs from this is a subagent request.
	orchRootSystem = "orch-eval-root: you are the root orchestrator"

	orchBoardKind = "board"
)

// OrchestrationScenario is one scripted multi-turn orchestration eval.
type OrchestrationScenario struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Kind is "board" for a direct TaskBoard API scenario (board tools are not
	// reachable from the loop yet; TaskBoardIO is only an interface for future
	// board_post / board_read tools). Everything else drives the real loop.
	Kind string `json:"kind,omitempty"`
	// Prompt is the first (root) prompt. FollowupPrompt, when set, is sent as a
	// second prompt on the SAME runner so a queued background result is drained
	// into that run's synthetic user turn.
	Prompt         string `json:"prompt"`
	FollowupPrompt string `json:"followup_prompt,omitempty"`
	// System, when set, is appended to the root system marker so scenarios can
	// still describe a root prompt without colliding with child prompts.
	System string `json:"system,omitempty"`
	// Personas are installed as a real ResolvePersona resolver on the task tool.
	Personas []OrchPersona `json:"personas,omitempty"`
	// Turns is the fake model script, in the order turns occur. Target selects
	// which participant consumes each entry.
	Turns      []OrchTurn      `json:"turns"`
	Assertions []OrchAssertion `json:"assertions"`
	// Board is used only by the direct API ("board") scenario.
	Board *OrchBoard `json:"board,omitempty"`
}

// OrchPersona is a named subagent definition handed to the real persona resolver.
type OrchPersona struct {
	Name         string   `json:"name"`
	System       string   `json:"system"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
}

// OrchTurn is one fake model reply. Respond reuses the core suite's wire type.
type OrchTurn struct {
	Target string `json:"target"`
	// Match lists substrings that must appear in the request that triggers this
	// turn (message text, tool names and tool arguments).
	Match []string `json:"match_request_contains,omitempty"`
	// Respond is the reply, reusing internal/eval's scripted response shape.
	Respond Respond `json:"respond"`
	// BlockUntilRelease gates a background child's reply on the harness's
	// release channel so the "ack first, result later" contract is observable.
	BlockUntilRelease bool `json:"block_until_release,omitempty"`
}

// OrchAssertion is one concrete check. Equals is used for counts and exact
// ordered lists (Values); Value is used for substring checks.
type OrchAssertion struct {
	Kind   string   `json:"kind"`
	Value  string   `json:"value,omitempty"`
	Equals *int     `json:"equals,omitempty"`
	Values []string `json:"values,omitempty"`
}

// OrchBoard describes a direct TaskBoard post/read flow.
type OrchBoard struct {
	Posts []OrchBoardPost `json:"posts,omitempty"`
	Reads []OrchBoardRead `json:"reads,omitempty"`
}

// OrchBoardPost is one post on the real TaskBoard.
type OrchBoardPost struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
	Body string `json:"body"`
}

// OrchBoardRead is one read on the real TaskBoard. Name labels the observation
// so an assertion can check its message count.
type OrchBoardRead struct {
	Name  string `json:"name"`
	Since int    `json:"since"`
	To    string `json:"to"`
}

// OrchestrationFailure is one failed scenario's diagnosis.
type OrchestrationFailure struct {
	ID     string
	Name   string
	Reason string
}

// OrchestrationReport summarizes a run. The field names are the ones the task
// asks for: Total, Passed, Failed, Failures.
type OrchestrationReport struct {
	Total    int
	Passed   int
	Failed   int
	Failures []OrchestrationFailure
}

// LoadOrchestration reads an orchestration scenario document from disk.
func LoadOrchestration(path string) ([]OrchestrationScenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	scenarios, err := ParseOrchestration(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return scenarios, nil
}

// ParseOrchestration decodes {"scenarios":[...]} or a bare array.
func ParseOrchestration(data []byte) ([]OrchestrationScenario, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	var scenarios []OrchestrationScenario
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(data, &scenarios); err != nil {
			return nil, fmt.Errorf("decode orchestration scenarios: %w", err)
		}
	} else {
		var doc struct {
			Scenarios []OrchestrationScenario `json:"scenarios"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("decode orchestration scenarios: %w", err)
		}
		scenarios = doc.Scenarios
	}
	if len(scenarios) == 0 {
		return nil, fmt.Errorf("no orchestration scenarios in document")
	}
	for i := range scenarios {
		if scenarios[i].Name == "" {
			scenarios[i].Name = fmt.Sprintf("orchestration scenario %d", i+1)
		}
	}
	return scenarios, nil
}

// orchRequest is the fake model's view of one request. Keeping phase and target
// lets the grader assert message roles and participant counts precisely.
type orchRequest struct {
	phase    string
	target   string
	messages []llm.Message
}

// orchChatter is the routing fake model. It mirrors scripted.go's Scripted but
// keeps one cursor per target instead of one global cursor, because the parent
// loop and its subagents share the same Chatter instance.
type orchChatter struct {
	mu         sync.Mutex
	rootSystem string
	turns      []OrchTurn
	cursors    map[string]int
	seq        int
	phase      string
	requests   []orchRequest
	violations []string

	// Background gating. When a scripted child turn sets BlockUntilRelease,
	// the reply waits here; started is signalled once the child enters it.
	release chan struct{}
	started chan struct{}
}

func newOrchChatter(rootSystem string, turns []OrchTurn) *orchChatter {
	return &orchChatter{
		rootSystem: rootSystem,
		turns:      append([]OrchTurn(nil), turns...),
		cursors:    make(map[string]int),
		phase:      "first",
	}
}

func (c *orchChatter) setPhase(phase string) {
	c.mu.Lock()
	c.phase = phase
	c.mu.Unlock()
}

// classify tells a root request from a child, and a verification child from
// the original child, using only the request bytes.
func (c *orchChatter) classify(messages []llm.Message) string {
	if len(messages) == 0 || messages[0].Role != "system" {
		return orchTargetParent
	}
	if messages[0].Content == c.rootSystem {
		return orchTargetParent
	}
	for _, msg := range messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "Verify the following result") {
			return orchTargetVerify
		}
	}
	return orchTargetChild
}

var _ llm.Chatter = (*orchChatter)(nil)

func (c *orchChatter) Chat(ctx context.Context, _ string, messages []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	if err := ctx.Err(); err != nil {
		return llm.Message{}, err
	}
	target := c.classify(messages)

	c.mu.Lock()
	c.seq++
	seq := c.seq
	c.requests = append(c.requests, orchRequest{
		phase:    c.phase,
		target:   target,
		messages: cloneMessages(messages),
	})
	cursor := c.cursors[target]
	var turn OrchTurn
	var ok bool
	for cursor < len(c.turns) {
		candidate := c.turns[cursor]
		cursor++
		if candidate.Target == target {
			turn = candidate
			ok = true
			break
		}
	}
	c.cursors[target] = cursor
	if ok {
		joined := joinMessages(messages)
		for _, needle := range turn.Match {
			if !strings.Contains(joined, needle) {
				c.violations = append(c.violations,
					fmt.Sprintf("%s turn: request does not contain %q", target, needle))
			}
		}
	}
	release, started := c.release, c.started
	c.mu.Unlock()

	if ok && turn.BlockUntilRelease && release != nil {
		if started != nil {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
			return llm.Message{}, ctx.Err()
		}
	}

	reply := llm.Message{Role: "assistant", Content: "the orchestration script ended"}
	if ok {
		reply = llm.Message{Role: "assistant", Content: turn.Respond.Text, Usage: turn.Respond.Usage}
		for i, call := range turn.Respond.ToolCalls {
			reply.ToolCalls = append(reply.ToolCalls, llm.ToolCall{
				ID:   fmt.Sprintf("orch-%d-%d", seq, i+1),
				Type: "function",
				Function: llm.ToolCallFunction{
					// Keep dispatch consistent with Scenario.Validate, which
					// checks the trimmed name: a padded name in a hand-written
					// script must reach the intended tool, not an unknown-tool
					// error from the registry's exact-name lookup.
					Name:      strings.TrimSpace(call.Name),
					Arguments: call.ArgumentsString(),
				},
			})
		}
	}
	if reply.Content != "" && onText != nil {
		onText(reply.Content)
	}
	return reply, nil
}

func (c *orchChatter) snapshot() ([]orchRequest, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	requests := make([]orchRequest, len(c.requests))
	copy(requests, c.requests)
	return requests, append([]string(nil), c.violations...)
}

// orchTools is the no-op ToolRunner for non-task tools. The task tool itself is
// wrapped around this runner by the real newToolset in internal/agent.
type orchTools struct{}

func (orchTools) Specs() []llm.ToolSpec {
	var spec llm.ToolSpec
	spec.Type = "function"
	spec.Function.Name = "read"
	spec.Function.Description = "fake read tool (never needed by these scripts)"
	spec.Function.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
	return []llm.ToolSpec{spec}
}

func (orchTools) Run(_ context.Context, name, args string) (string, error) {
	return "ok: " + name + " " + args, nil
}

// noticeSink collects parent notices; background children emit from their own
// goroutine, so it is mutex protected.
type noticeSink struct {
	mu      sync.Mutex
	notices []string
}

func (s *noticeSink) add(e agent.Event) {
	if e.Kind != agent.EventNotice {
		return
	}
	s.mu.Lock()
	s.notices = append(s.notices, e.Text)
	s.mu.Unlock()
}

func (s *noticeSink) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.notices...)
}

// orchObservation is the harness-owned view of one scenario run.
type orchObservation struct {
	finalText       string
	parentToolCalls []string
	parentTurns     int
	lastParentRoles []string
	parentToolTexts []string
	childSystems    []string
	childPrompts    []string
	verifyPrompts   []string
	childRuns       int
	verifyRuns      int
	notices         []string
	violations      []string

	followupRoles []string
	followupUsers []string

	queuedAtAck   int
	bgResultSeen  int
	boardPosts    int
	boardBodies   []string
	boardReadSize map[string]int
}

// RunOrchestration runs every scenario through the real agent loop and returns
// a complete report. One failing scenario never stops the suite.
func RunOrchestration(ctx context.Context, scenarios []OrchestrationScenario) OrchestrationReport {
	report := OrchestrationReport{
		Total:    len(scenarios),
		Failures: make([]OrchestrationFailure, 0),
	}
	for _, scenario := range scenarios {
		observation, runErr := runOneOrchestration(ctx, scenario)
		reason := ""
		switch {
		case runErr != nil:
			reason = "runner error: " + runErr.Error()
		case len(observation.violations) > 0:
			reason = observation.violations[0]
		default:
			reason = checkOrchestration(scenario, observation)
		}
		if reason == "" {
			report.Passed++
		} else {
			report.Failed++
			report.Failures = append(report.Failures, OrchestrationFailure{
				ID: scenario.ID, Name: scenario.Name, Reason: reason,
			})
		}
	}
	return report
}

// runOneOrchestration drives the real agent loop (or the direct board API) for
// one scenario and collects the observation the assertions grade.
func runOneOrchestration(ctx context.Context, s OrchestrationScenario) (orchObservation, error) {
	if s.Kind == orchBoardKind {
		return runBoardScenario(s)
	}

	rootSystem := strings.TrimSpace(orchRootSystem + " " + s.System)
	chatter := newOrchChatter(rootSystem, s.Turns)
	notices := &noticeSink{}

	// Real persona resolver, real background queue. NewChild is intentionally
	// left nil so the task tool builds real *agent.Runner child loops.
	personas := make(map[string]agent.SubagentConfig, len(s.Personas))
	for _, p := range s.Personas {
		personas[p.Name] = agent.SubagentConfig{
			System:       strings.TrimSpace(p.System),
			AllowedTools: append([]string(nil), p.AllowedTools...),
		}
	}
	bg := agent.NewBackgroundResults(0, 4)
	if s.FollowupPrompt != "" {
		chatter.release = make(chan struct{})
		chatter.started = make(chan struct{}, 1)
	}

	opts := agent.Options{
		Model:         "orch-scripted",
		MaxTurns:      20,
		System:        rootSystem,
		Chatter:       chatter,
		Tools:         orchTools{},
		Budget:        agent.Budget{MaxTurns: 20},
		NoFinalAnswer: true,
		Emit:          notices.add,
		Subagent: &agent.SubagentConfig{
			MaxDepth: agent.DefaultMaxDepth,
			ResolvePersona: func(name string) (agent.SubagentConfig, bool) {
				cfg, ok := personas[name]
				return cfg, ok
			},
			Background: bg,
		},
	}
	runner := &agent.Runner{Opts: opts}

	first, err := runner.Run(ctx, s.Prompt)
	obs := orchObservation{finalText: first.Text, boardReadSize: make(map[string]int)}

	if s.FollowupPrompt == "" {
		obs = collectRequests(obs, chatter, "")
		obs.notices = notices.all()
		return obs, err
	}
	// Background scenario: the first run must return while the child is still
	// blocked in the fake model. Only then do we release it and send a second
	// prompt, which drains the queued result into a synthetic user turn.
	select {
	case <-chatter.started:
	case <-time.After(3 * time.Second):
		return obs, fmt.Errorf("background child never started")
	}
	obs.queuedAtAck = bg.Len()
	close(chatter.release)
	if !waitUntil(func() bool { return bg.Len() > 0 }, 3*time.Second) {
		return obs, fmt.Errorf("background result was never queued")
	}
	obs.bgResultSeen = 1

	chatter.setPhase("followup")
	_, err2 := runner.Run(ctx, s.FollowupPrompt)
	obs = collectRequests(obs, chatter, "followup")
	obs.notices = notices.all()
	if err != nil {
		return obs, err
	}
	return obs, err2
}

// collectRequests folds the fake model's recorded requests and the run result
// into the observation. phaseFilter "" collects the first-run requests.
func collectRequests(obs orchObservation, chatter *orchChatter, phaseFilter string) orchObservation {
	requests, violations := chatter.snapshot()
	obs.violations = append(obs.violations, violations...)
	seenCalls := make(map[string]bool)
	for _, request := range requests {
		if request.phase == "followup" {
			if phaseFilter == "followup" && len(obs.followupRoles) == 0 {
				obs.followupRoles = rolesOf(request.messages)
				obs.followupUsers = userTexts(request.messages)
			}
			continue
		}
		if request.target == orchTargetParent {
			obs.parentTurns++
			obs.lastParentRoles = rolesOf(request.messages)
			for _, msg := range request.messages {
				if msg.Role == "tool" {
					obs.parentToolTexts = append(obs.parentToolTexts, msg.Content)
				}
				for _, call := range msg.ToolCalls {
					if !seenCalls[call.ID] {
						seenCalls[call.ID] = true
						obs.parentToolCalls = append(obs.parentToolCalls, call.Function.Name)
					}
				}
			}
			continue
		}
		if request.target == orchTargetVerify {
			obs.verifyRuns++
			obs.verifyPrompts = append(obs.verifyPrompts, lastUserText(request.messages))
			continue
		}
		obs.childRuns++
		if len(request.messages) > 0 {
			obs.childSystems = append(obs.childSystems, request.messages[0].Content)
		}
		obs.childPrompts = append(obs.childPrompts, lastUserText(request.messages))
	}
	return obs
}

// runBoardScenario exercises the real TaskBoard API directly. There are no
// board_post / board_read tools in the loop yet (TaskBoardIO is only a future
// seam), so this is intentionally a direct API scenario.
func runBoardScenario(s OrchestrationScenario) (orchObservation, error) {
	obs := orchObservation{boardReadSize: make(map[string]int)}
	if s.Board == nil {
		return obs, fmt.Errorf("board scenario has no board definition")
	}
	board := &agent.TaskBoard{}
	for _, post := range s.Board.Posts {
		if _, err := board.Post(post.From, post.To, post.Kind, post.Body); err != nil {
			return obs, fmt.Errorf("board post %q -> %q: %w", post.From, post.To, err)
		}
		obs.boardPosts++
		obs.boardBodies = append(obs.boardBodies, post.Body)
	}
	for _, read := range s.Board.Reads {
		obs.boardReadSize[read.Name] = len(board.Read(read.Since, read.To))
	}
	return obs, nil
}

// checkOrchestration grades one observation and returns "" when every assertion
// holds. Every failure is concrete: a count, a substring or a message role.
func checkOrchestration(s OrchestrationScenario, obs orchObservation) string {
	for _, assertion := range s.Assertions {
		if reason := checkAssertion(assertion, obs); reason != "" {
			return reason
		}
	}
	return ""
}

func checkAssertion(a OrchAssertion, obs orchObservation) string {
	switch a.Kind {
	case "child_runs", "task_attempts":
		return wantInt(a.Kind, a.Equals, obs.childRuns)
	case "verify_runs":
		return wantInt(a.Kind, a.Equals, obs.verifyRuns)
	case "parent_turns":
		return wantInt(a.Kind, a.Equals, obs.parentTurns)
	case "tool_calls":
		return wantStrings(a.Kind, a.Values, obs.parentToolCalls)
	case "final_text_contains":
		return wantSubstring(a.Kind, a.Value, obs.finalText, true)
	case "final_text_not_contains":
		return wantSubstring(a.Kind, a.Value, obs.finalText, false)
	case "child_system_contains":
		return wantAnySubstring(a.Kind, a.Value, obs.childSystems, true)
	case "child_prompt_contains":
		return wantAnySubstring(a.Kind, a.Value, obs.childPrompts, true)
	case "verify_prompt_contains":
		return wantAnySubstring(a.Kind, a.Value, obs.verifyPrompts, true)
	case "notice_contains":
		return wantAnySubstring(a.Kind, a.Value, obs.notices, true)
	case "notice_count_contains":
		count := 0
		for _, notice := range obs.notices {
			if strings.Contains(notice, a.Value) {
				count++
			}
		}
		return wantInt(a.Kind+" containing "+a.Value, a.Equals, count)
	case "parent_roles":
		return wantStrings(a.Kind, a.Values, obs.lastParentRoles)
	case "parent_tool_result_contains":
		return wantAnySubstring(a.Kind, a.Value, obs.parentToolTexts, true)
	case "followup_roles":
		return wantStrings(a.Kind, a.Values, obs.followupRoles)
	case "followup_user_contains":
		return wantAnySubstring(a.Kind, a.Value, obs.followupUsers, true)
	case "bg_result_queued":
		return wantInt(a.Kind, a.Equals, obs.bgResultSeen)
	case "queued_at_ack":
		return wantInt(a.Kind, a.Equals, obs.queuedAtAck)
	case "board_post_count":
		return wantInt(a.Kind, a.Equals, obs.boardPosts)
	case "board_message_contains":
		return wantAnySubstring(a.Kind, a.Value, obs.boardBodies, true)
	case "board_read_count":
		return wantInt(a.Kind+" "+a.Value, a.Equals, obs.boardReadSize[a.Value])
	default:
		return fmt.Sprintf("unknown assertion kind %q", a.Kind)
	}
}

func wantInt(kind string, want *int, got int) string {
	if want == nil {
		return kind + " assertion has no equals value"
	}
	if got != *want {
		return fmt.Sprintf("%s = %d, want %d", kind, got, *want)
	}
	return ""
}

func wantStrings(kind string, want, got []string) string {
	if want == nil {
		return kind + " assertion has no values"
	}
	if !equalStrings(want, got) {
		return fmt.Sprintf("%s = %v, want %v", kind, nilOr(got), want)
	}
	return ""
}

func wantSubstring(kind, needle, haystack string, present bool) string {
	has := strings.Contains(haystack, needle)
	if has != present {
		if present {
			return fmt.Sprintf("%s: %q missing from %q", kind, needle, clip(haystack, 200))
		}
		return fmt.Sprintf("%s: %q must not be in %q", kind, needle, clip(haystack, 200))
	}
	return ""
}

func wantAnySubstring(kind, needle string, haystack []string, present bool) string {
	for _, item := range haystack {
		if strings.Contains(item, needle) {
			if present {
				return ""
			}
			return fmt.Sprintf("%s: %q must not be in %v", kind, needle, haystack)
		}
	}
	if present {
		return fmt.Sprintf("%s: %q missing from %v", kind, needle, haystack)
	}
	return ""
}

func cloneMessages(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, len(messages))
	copy(out, messages)
	return out
}

func joinMessages(messages []llm.Message) string {
	parts := make([]string, 0, len(messages))
	for _, msg := range messages {
		if msg.Content != "" {
			parts = append(parts, msg.Content)
		}
		for _, call := range msg.ToolCalls {
			parts = append(parts, call.Function.Name+" "+call.Function.Arguments)
		}
	}
	return strings.Join(parts, "\n")
}

func rolesOf(messages []llm.Message) []string {
	roles := make([]string, 0, len(messages))
	for _, msg := range messages {
		roles = append(roles, msg.Role)
	}
	return roles
}

func userTexts(messages []llm.Message) []string {
	var out []string
	for _, msg := range messages {
		if msg.Role == "user" {
			out = append(out, msg.Content)
		}
	}
	return out
}

func lastUserText(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

func waitUntil(condition func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return condition()
}
