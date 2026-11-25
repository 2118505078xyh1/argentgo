package eval

// 端到端编排 eval。
//
// 与核心套件（其 fakeRun 是循环的手写替身）不同，
// 此 harness 驱动真实的 agent 循环和真实的 task 工具。唯一注入的假对象是真实 harness 总会注入的：一个假模型
// 注入的假对象是真实 harness 总会注入的：假模型
// （llm.Chatter，如 scripted.go 的 Scripted）和一个用于非 task 工具的 no-op ToolRunner。
// Persona 解析、PARENT-RECHECK 验证、有界重试循环、后台队列/排空以及 TaskBoard 全都是
// 重试循环、后台队列/排空以及 TaskBoard 全都是真实的
// 来自 internal/agent 的真实实现。
//
// 假模型是*路由式*的而非单一全局队列：子代理
// 假模型是*路由式*的而非单一全局队列：子代理与其父级共享 Chatter，因此脚本必须按参与者
// （根/子/验证子）选择回复。scripted.go 中的 Scripted 做不到；
// 本文件复用其 Respond/ToolCall 线上类型，但按请求的
// 系统提示路由每一轮。这让单个 JSON 文档能描述整个多轮委派，包括交错在
// 描述整个多轮委派，包括交错在父级各轮之间的
// 子级轮次。
//
// 父级各轮之间的子级轮次。
// 无网络、无真实模型、无 cgo。后台场景用 channel 门控子级回复，使"先确认、后结果"是确定的。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/llm"
)

// 脚本目标。"verify" 也是子级（它运行嵌套循环），但
// 脚本目标。"verify" 也是子级（它运行嵌套循环），但因验证提示很独特而被单独分类。
const (
	orchTargetParent = "parent"
	orchTargetChild  = "child"
	orchTargetVerify = "verify"

	// orchRootSystem 是根请求的系统提示。系统消息与之不同的任何请求都是子代理请求。
	// 系统消息与之不同的任何请求都是子代理请求。
	orchRootSystem = "orch-eval-root: you are the root orchestrator"

	orchBoardKind = "board"
)

// OrchestrationScenario 是一个脚本化多轮编排 eval。
type OrchestrationScenario struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Kind 对直接的 TaskBoard API 场景为 "board"（board 工具尚不可从循环到达；TaskBoardIO 只是为将来
	// 尚不可从循环到达；TaskBoardIO 只是为将来准备的
	// board_post / board_read 工具准备的接口）。其他情况都驱动真实循环。
	Kind string `json:"kind,omitempty"`
	// Prompt 是第一个（根）提示。FollowupPrompt 设置后作为同一 runner 上的
	// 同一 runner 上的第二个提示，使排队的后台结果被排入
	// 第二个提示发送，使排队的后台结果被排入该运行的合成用户轮次。
	Prompt         string `json:"prompt"`
	FollowupPrompt string `json:"followup_prompt,omitempty"`
	// System 设置后追加到根系统标记，使场景仍能描述根提示而不与子提示冲突。
	// 仍能描述根提示而不与子提示冲突。
	System string `json:"system,omitempty"`
	// Personas 作为真实的 ResolvePersona resolver 安装到 task 工具上。
	Personas []OrchPersona `json:"personas,omitempty"`
	// Turns 是假模型脚本，按轮次发生的顺序。Target 选择哪个参与者消费每条记录。
	// 哪个参与者消费每条记录。
	Turns      []OrchTurn      `json:"turns"`
	Assertions []OrchAssertion `json:"assertions"`
	// Board 仅被直接的 API（"board"）场景使用。
	Board *OrchBoard `json:"board,omitempty"`
}

// OrchPersona 是交给真实 persona resolver 的具名子代理定义。
type OrchPersona struct {
	Name         string   `json:"name"`
	System       string   `json:"system"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
}

// OrchTurn 是一条假模型回复。Respond 复用核心套件的线上类型。
type OrchTurn struct {
	Target string `json:"target"`
	// Match 列出必须出现在触发此轮的请求中的子串（消息文本、工具名和工具参数）。
	// 此轮的请求中的子串（消息文本、工具名和工具参数）。
	Match []string `json:"match_request_contains,omitempty"`
	// Respond 是回复，复用 internal/eval 的脚本化响应形态。
	Respond Respond `json:"respond"`
	// BlockUntilRelease 用 harness 的 release channel 门控后台子级的回复，
	// BlockUntilRelease 用 harness 的 release channel 门控后台子级的回复，使"先确认、后结果"契约可观察。
	BlockUntilRelease bool `json:"block_until_release,omitempty"`
}

// OrchAssertion 是一个具体检查。Equals 用于计数和精确
// OrchAssertion 是一个具体检查。Equals 用于计数和精确有序列表（Values）；Value 用于子串检查。
type OrchAssertion struct {
	Kind   string   `json:"kind"`
	Value  string   `json:"value,omitempty"`
	Equals *int     `json:"equals,omitempty"`
	Values []string `json:"values,omitempty"`
}

// OrchBoard 描述一个直接的 TaskBoard 发布/读取流程。
type OrchBoard struct {
	Posts []OrchBoardPost `json:"posts,omitempty"`
	Reads []OrchBoardRead `json:"reads,omitempty"`
}

// OrchBoardPost 是真实 TaskBoard 上的一次发布。
type OrchBoardPost struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
	Body string `json:"body"`
}

// OrchBoardRead 是真实 TaskBoard 上的一次读取。Name 标记该观察，使断言能检查其消息数。
// 使断言能检查其消息数。
type OrchBoardRead struct {
	Name  string `json:"name"`
	Since int    `json:"since"`
	To    string `json:"to"`
}

// OrchestrationFailure 是一个失败场景的诊断。
type OrchestrationFailure struct {
	ID     string
	Name   string
	Reason string
}

// OrchestrationReport 汇总一次运行。字段名是任务
// OrchestrationReport 汇总一次运行。字段名是任务要求的：Total、Passed、Failed、Failures。
type OrchestrationReport struct {
	Total    int
	Passed   int
	Failed   int
	Failures []OrchestrationFailure
}

// LoadOrchestration 从磁盘读取编排场景文档。
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

// ParseOrchestration 解码 {"scenarios":[...]} 或裸数组。
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

// orchRequest 是假模型对一次请求的视图。保留 phase 和 target 使评分器能精确断言消息角色和参与者数量。
// 使评分器能精确断言消息角色和参与者数量。
type orchRequest struct {
	phase    string
	target   string
	messages []llm.Message
}

// orchChatter 是路由式假模型。它镜像 scripted.go 的 Scripted，但为每个 target 保留一个游标而非单一全局游标，因为父级
// 为每个 target 保留一个游标而非单一全局游标，因为父级
// 循环及其子代理共享同一个 Chatter 实例。
type orchChatter struct {
	mu         sync.Mutex
	rootSystem string
	turns      []OrchTurn
	cursors    map[string]int
	seq        int
	phase      string
	requests   []orchRequest
	violations []string

	// 后台门控。当脚本化子级轮次设置 BlockUntilRelease 时，
	// 后台门控。当脚本化子级轮次设置 BlockUntilRelease 时，回复在此等待；子级进入时发信号 started。
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

// classify 仅用请求字节区分根请求与子级、验证子级与原始子级。
// 与原始子级，仅用请求字节。
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
					// 保持分发与 Scenario.Validate 一致，后者检查裁剪后的名称：手写脚本中带填充的名称必须到达目标工具，而不是 registry 精确名称查找产生的
					// 检查裁剪后的名称：手写脚本中带填充的名称
					// 必须到达目标工具，而不是未知工具
					// 未知工具错误。
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

// orchTools 是用于非 task 工具的 no-op ToolRunner。task 工具本身由 internal/agent 中真实的 newToolset 包装在这个 runner 之上。
// 由 internal/agent 中真实的 newToolset 包装在这个 runner 之上。
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

// noticeSink 收集父级通知；后台子级从自身 goroutine 发出，因此用
// noticeSink 收集父级通知；后台子级从自身 goroutine 发出，因此用互斥锁保护。
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

// orchObservation 是 harness 拥有的某次场景运行的视图。
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

// RunOrchestration 通过真实 agent 循环运行每个场景并返回完整报告。一个失败的场景永不停止套件。
// 完整报告。一个失败的场景永不停止套件。
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

// runOneOrchestration 为一个场景驱动真实 agent 循环（或直接的 board API），并收集断言要评分的观察结果。
// 一个场景并收集断言要评分的观察结果。
func runOneOrchestration(ctx context.Context, s OrchestrationScenario) (orchObservation, error) {
	if s.Kind == orchBoardKind {
		return runBoardScenario(s)
	}

	rootSystem := strings.TrimSpace(orchRootSystem + " " + s.System)
	chatter := newOrchChatter(rootSystem, s.Turns)
	notices := &noticeSink{}

	// 真实的 persona resolver、真实的后台队列。NewChild 有意
	// 真实的 persona resolver、真实的后台队列。NewChild 有意留空，使 task 工具构建真实的 *agent.Runner 子循环。
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
	// 后台场景：第一次运行必须在子级仍
	// 阻塞于假模型时返回。只有随后我们释放它并发送第二个
	// 后台场景：第一次运行必须在子级仍阻塞于假模型时返回。只有随后我们释放它并发送第二个
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

// 提示，把排队结果排入合成用户轮次。
// collectRequests 把假模型记录的请求与运行结果折叠进观察。phaseFilter "" 收集第一次运行的请求。
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

// runBoardScenario 直接演练真实 TaskBoard API。没有
// runBoardScenario 直接演练真实 TaskBoard API。循环中还没有 board_post / board_read 工具（TaskBoardIO 只是未来的
// 接缝），因此这有意是一个直接的 API 场景。
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

// checkOrchestration 对一个观察评分，所有断言成立时返回 ""。每个失败都是具体的：计数、子串或消息角色。
// 成立时返回 ""。每个失败都是具体的：计数、子串或消息角色。
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
