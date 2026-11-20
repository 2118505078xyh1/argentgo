package agent

import (
	"context"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestBudgetBreach(t *testing.T) {
	usage := llm.Usage{PromptTokens: 60, CompletionTokens: 60}
	cases := []struct {
		name       string
		budget     Budget
		lastPrompt int
		cost       float64
		costKnown  bool
		want       string
	}{
		{"under", Budget{MaxTotalTokens: 500, MaxCostUSD: 1, MaxPromptTokens: 500}, 100, 0.1, true, ""},
		{"total", Budget{MaxTotalTokens: 100}, 10, 0, false, StopMaxTotalTokens},
		{"cost", Budget{MaxCostUSD: 0.05}, 10, 0.1, true, StopMaxCost},
		{"cost unknown", Budget{MaxCostUSD: 0.05}, 10, 0.1, false, ""},
		{"prompt", Budget{MaxPromptTokens: 50}, 100, 0, false, StopMaxPromptTokens},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, detail, hit := tc.budget.Breach(usage, tc.lastPrompt, tc.cost, tc.costKnown)
			if reason != tc.want {
				t.Fatalf("reason = %q, want %q (%s)", reason, tc.want, detail)
			}
			if hit != (tc.want != "") {
				t.Fatalf("hit = %v, want %v", hit, tc.want != "")
			}
			if hit && detail == "" {
				t.Fatal("a breach must carry a detail line")
			}
		})
	}
}

func TestPriceForFallsBackToTheDefaultEntry(t *testing.T) {
	prices := map[string]Price{"": {Input: 1, Output: 2}, "special": {Input: 10, Output: 20}}
	if p, ok := PriceFor(prices, "special"); !ok || p.Input != 10 {
		t.Fatalf("special price = %+v, %v", p, ok)
	}
	if p, ok := PriceFor(prices, "unknown"); !ok || p.Input != 1 {
		t.Fatalf("fallback price = %+v, %v", p, ok)
	}
	if _, ok := PriceFor(nil, "any"); ok {
		t.Fatal("an empty price table has no prices")
	}
	if cost := (Price{Input: 1_000_000}).Cost(llm.Usage{PromptTokens: 2}); cost != 2 {
		t.Fatalf("cost = %v, want 2", cost)
	}
}

func TestMaxTotalTokensStopsBeforeTheToolBatch(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		reply := toolReply(toolCall("c1", "read", `{}`))
		reply.Usage = &llm.Usage{PromptTokens: 500, CompletionTokens: 600}
		return reply, nil
	}}
	runner := &Runner{Opts: Options{
		Model:   "m",
		Chatter: chat,
		Tools:   tools,
		Budget:  Budget{MaxTotalTokens: 900},
	}}

	res, err := runner.Run(context.Background(), "read")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopMaxTotalTokens {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopMaxTotalTokens)
	}
	if tools.count() != 0 {
		t.Fatalf("tool ran despite an exhausted budget")
	}
	if res.Usage.PromptTokens != 500 {
		t.Fatalf("usage = %+v", res.Usage)
	}
}

func TestMaxCostStopsRun(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		reply := toolReply(toolCall("c1", "read", `{}`))
		reply.Usage = &llm.Usage{PromptTokens: 1000, CompletionTokens: 1000}
		return reply, nil
	}}
	runner := &Runner{Opts: Options{
		Model:   "m",
		Chatter: chat,
		Tools:   tools,
		Budget:  Budget{MaxCostUSD: 0.5},
		Prices:  map[string]Price{"m": {Input: 1000, Output: 1000}},
	}}

	res, err := runner.Run(context.Background(), "read")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopMaxCost {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopMaxCost)
	}
	if tools.count() != 0 {
		t.Fatal("tool ran despite an exhausted budget")
	}
}

func TestMaxPromptTokensStopsAfterAnOversizedPrompt(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		reply := toolReply(toolCall("c1", "read", `{}`))
		reply.Usage = &llm.Usage{PromptTokens: 5000}
		return reply, nil
	}}
	runner := &Runner{Opts: Options{
		Model:   "m",
		Chatter: chat,
		Tools:   tools,
		Budget:  Budget{MaxPromptTokens: 4000},
	}}

	res, err := runner.Run(context.Background(), "read")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopMaxPromptTokens {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopMaxPromptTokens)
	}
}

func TestMaxPromptTokensStopsBeforeTheFirstRequest(t *testing.T) {
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		t.Fatal("chatter must not run when the prompt cannot fit")
		return llm.Message{}, nil
	}}
	runner := &Runner{Opts: Options{
		Model:   "m",
		System:  "rules and more rules",
		Chatter: chat,
		Tools:   &fakeTools{},
		Budget:  Budget{MaxPromptTokens: 1},
	}}

	res, err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopMaxPromptTokens || res.Turns != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestParseTurnBudgetRecognisedForms(t *testing.T) {
	cases := []struct {
		prompt string
		want   int64
	}{
		{"+500k", 500_000},
		{"+2m", 2_000_000},
		{"+500K", 500_000},
		{"+1.5m", 1_500_000},
		{"+ 500k", 500_000},
		{"please +750k tokens for this task", 750_000},
		{"use 2m tokens", 2_000_000},
		{"USE 2M TOKENS", 2_000_000},
		{"use 1.5m tokens", 1_500_000},
		{"use 2m token", 2_000_000},
		{"use 2m output tokens", 2_000_000},
		{"budget 2m tokens", 2_000_000},
		{"spend 500k tokens", 500_000},
		{"cap 200k tokens", 200_000},
		{"allocate 3m tokens for the refactor", 3_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.prompt, func(t *testing.T) {
			got, ok := ParseTurnBudget(tc.prompt)
			if !ok || got != tc.want {
				t.Fatalf("ParseTurnBudget(%q) = %d, %v; want %d, true", tc.prompt, got, ok, tc.want)
			}
		})
	}
}

func TestParseTurnBudgetClampsToRange(t *testing.T) {
	cases := []struct {
		prompt string
		want   int64
	}{
		{"+1k", 1_000},
		{"+100m", 100_000_000},
		{"+0.1k", 1_000},                         // 100 向上收紧到 1k
		{"+0.5m", 500_000},                       // 无需收紧
		{"+200m", 100_000_000},                   // 向下收紧到 100m
		{"+999999999999999999999m", 100_000_000}, // 先饱和，再收紧
	}
	for _, tc := range cases {
		t.Run(tc.prompt, func(t *testing.T) {
			got, ok := ParseTurnBudget(tc.prompt)
			if !ok || got != tc.want {
				t.Fatalf("ParseTurnBudget(%q) = %d, %v; want %d, true", tc.prompt, got, ok, tc.want)
			}
		})
	}
}

func TestParseTurnBudgetIgnoresInjectedBlocks(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   int64
		ok     bool
	}{
		{
			name:   "system reminder before real declaration",
			prompt: "<system-reminder>use 9m tokens</system-reminder>\nuse 2m tokens",
			want:   2_000_000,
			ok:     true,
		},
		{
			name:   "declaration only inside system reminder",
			prompt: "<system-reminder>use 9m tokens</system-reminder>",
			ok:     false,
		},
		{
			name:   "fenced code block",
			prompt: "```\n+999m\n```\n+500k",
			want:   500_000,
			ok:     true,
		},
		{
			name:   "tilde fenced code block",
			prompt: "~~~\nuse 9m tokens\n~~~\nuse 2m tokens",
			want:   2_000_000,
			ok:     true,
		},
		{
			name:   "inline code span",
			prompt: "see `@src/x.go +999m` for details\n+500k",
			want:   500_000,
			ok:     true,
		},
		{
			name:   "file attachment block",
			prompt: "<file path=\"big.txt\">\n+999m tokens\n</file>\n+500k",
			want:   500_000,
			ok:     true,
		},
		{
			name:   "declaration only inside file attachment",
			prompt: "<file path=\"big.txt\">use 9m tokens</file>",
			ok:     false,
		},
		{
			name:   "project memory with nested file",
			prompt: "<project-memory><file path=\"x\">+999m</file></project-memory>\n+500k",
			want:   500_000,
			ok:     true,
		},
		{
			name:   "at file reference swallows attached number",
			prompt: "@src/big.txt +999m tokens\n+500k",
			want:   500_000,
			ok:     true,
		},
		{
			name:   "declaration only after at reference",
			prompt: "please check @attachment.md +999m tokens",
			ok:     false,
		},
		{
			name:   "html tags are stripped but user text survives",
			prompt: "<b>+500k</b> for this turn",
			want:   500_000,
			ok:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseTurnBudget(tc.prompt)
			if ok != tc.ok || (tc.ok && got != tc.want) {
				t.Fatalf("ParseTurnBudget() = %d, %v; want %d, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestParseTurnBudgetNoDeclaration(t *testing.T) {
	cases := []string{
		"",
		"just a normal prompt with 500k files",
		"use 500 tokens",
		"+ 500",
		"I have 2 million tokens",
		"git commit -500k",
		"500miles away",
		"the file has 2m lines",
	}
	for _, prompt := range cases {
		t.Run(prompt, func(t *testing.T) {
			if got, ok := ParseTurnBudget(prompt); ok {
				t.Fatalf("ParseTurnBudget(%q) = %d, true; want ok=false", prompt, got)
			}
		})
	}
}

func TestDeclaredTurnTokensParticipateInBreach(t *testing.T) {
	usage := llm.Usage{PromptTokens: 600, CompletionTokens: 400}

	reason, detail, hit := (Budget{DeclaredTurnTokens: 1_000}).Breach(usage, 10, 0, false)
	if reason != StopDeclaredTurnBudget || !hit || detail == "" {
		t.Fatalf("at limit: reason=%q hit=%v detail=%q", reason, hit, detail)
	}

	reason, _, hit = (Budget{DeclaredTurnTokens: 1_001}).Breach(usage, 10, 0, false)
	if reason != "" || hit {
		t.Fatalf("under limit: reason=%q hit=%v", reason, hit)
	}

	// 声明的上限绝不会放宽既有的限制（用量总量为 1000）。
	reason, _, hit = (Budget{MaxTotalTokens: 500, DeclaredTurnTokens: 2_000}).Breach(usage, 10, 0, false)
	if reason != StopMaxTotalTokens || !hit {
		t.Fatalf("existing limit still wins: reason=%q hit=%v", reason, hit)
	}

	if child := (Budget{DeclaredTurnTokens: 1_000}).sliceForChild(); child.DeclaredTurnTokens != 500 {
		t.Fatalf("child declared budget = %d, want 500", child.DeclaredTurnTokens)
	}
}

func TestParsedDeclarationFeedsBudget(t *testing.T) {
	tokens, ok := ParseTurnBudget("use 2m tokens")
	if !ok || tokens != 2_000_000 {
		t.Fatalf("parse = %d, %v", tokens, ok)
	}
	usage := llm.Usage{PromptTokens: 1_900_000, CompletionTokens: 100_000}
	reason, _, hit := (Budget{DeclaredTurnTokens: tokens}).Breach(usage, 10, 0, false)
	if reason != StopDeclaredTurnBudget || !hit {
		t.Fatalf("reason=%q hit=%v; want declared budget breach", reason, hit)
	}
}

func TestEstimatedUsageFeedsTheBudgetWithoutProviderCounts(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		// 回复中没有 Usage：循环必须回退到它的估算值。
		return toolReply(toolCall("c1", "read", `{}`)), nil
	}}
	runner := &Runner{Opts: Options{
		Model:   "m",
		System:  "short",
		Chatter: chat,
		Tools:   tools,
		Budget:  Budget{MaxTotalTokens: 5},
	}}

	res, err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopMaxTotalTokens {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopMaxTotalTokens)
	}
	if res.Usage.PromptTokens == 0 {
		t.Fatal("estimated usage was not recorded")
	}
}
