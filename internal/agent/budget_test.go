package agent

import (
	"context"
	"testing"

	"github.com/2118505078xyh1/argent-go/internal/llm"
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

func TestEstimatedUsageFeedsTheBudgetWithoutProviderCounts(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		// No Usage on the reply: the loop must fall back to its estimate.
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
