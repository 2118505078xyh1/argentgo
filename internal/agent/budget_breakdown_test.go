package agent

import (
	"math"
	"strings"
	"testing"
)

func TestBudgetBreakdownAttributesEachContributor(t *testing.T) {
	blocks := BudgetBreakdown(
		strings.Repeat("a", 40),            // 10 tokens
		strings.Repeat("b", 80),            // 20 tokens
		[]string{strings.Repeat("c", 200)}, // 50 tokens
	)

	want := map[string]int{
		BudgetBlockSystem:        10,
		BudgetBlockHistory:       20,
		strings.Repeat("c", 200): 50,
	}
	if len(blocks) != len(want) {
		t.Fatalf("got %d blocks, want %d: %+v", len(blocks), len(want), blocks)
	}
	for _, block := range blocks {
		expected, ok := want[block.Name]
		if !ok {
			t.Fatalf("unexpected block %q", block.Name)
		}
		if block.Tokens != expected {
			t.Errorf("block %q tokens = %d, want %d", block.Name, block.Tokens, expected)
		}
	}
}

func TestBudgetBreakdownCountsCJKRunesAsOneTokenEach(t *testing.T) {
	system := strings.Repeat("价", 7) // 7 wide runes -> 7, not ceil(7/4)=2
	blocks := BudgetBreakdown(system, "", nil)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1: %+v", len(blocks), blocks)
	}
	if blocks[0].Name != BudgetBlockSystem {
		t.Fatalf("block name = %q, want %q", blocks[0].Name, BudgetBlockSystem)
	}
	if blocks[0].Tokens != 7 {
		t.Fatalf("CJK tokens = %d, want 7", blocks[0].Tokens)
	}
	if math.Abs(blocks[0].Percent-100) > 1e-9 {
		t.Fatalf("percent = %v, want 100", blocks[0].Percent)
	}
}

func TestBudgetBreakdownSortsByTokensAndSumsToHundred(t *testing.T) {
	blocks := BudgetBreakdown("", "", []string{
		strings.Repeat("a", 400), // 100 tokens
		strings.Repeat("b", 160), // 40 tokens
		strings.Repeat("c", 240), // 60 tokens
	})

	wantTokens := []int{100, 60, 40}
	if len(blocks) != len(wantTokens) {
		t.Fatalf("got %d blocks, want %d: %+v", len(blocks), len(wantTokens), blocks)
	}
	sum := 0.0
	for i, want := range wantTokens {
		if blocks[i].Tokens != want {
			t.Errorf("block %d tokens = %d, want %d", i, blocks[i].Tokens, want)
		}
		sum += blocks[i].Percent
	}
	if math.Abs(sum-100) > 1e-9 {
		t.Fatalf("percent sum = %v, want 100", sum)
	}
	wantPercent := []float64{50, 30, 20}
	for i, want := range wantPercent {
		if math.Abs(blocks[i].Percent-want) > 1e-9 {
			t.Errorf("block %d percent = %v, want %v", i, blocks[i].Percent, want)
		}
	}
}

func TestBudgetBreakdownEmptyInputReturnsNoBlocks(t *testing.T) {
	blocks := BudgetBreakdown("", "", nil)
	if len(blocks) != 0 {
		t.Fatalf("empty input produced blocks: %+v", blocks)
	}

	blocks = BudgetBreakdown("", "", []string{"", ""})
	if len(blocks) != 0 {
		t.Fatalf("empty file entries produced blocks: %+v", blocks)
	}
}

func TestBudgetBreakdownSkipsEmptyContributors(t *testing.T) {
	blocks := BudgetBreakdown("", strings.Repeat("x", 8), []string{"", "note"})
	// history: ceil(8/4)=2; "note": ceil(4/4)=1 -> history first.
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2: %+v", len(blocks), blocks)
	}
	if blocks[0].Name != BudgetBlockHistory || blocks[0].Tokens != 2 {
		t.Fatalf("first block = %+v, want history/2", blocks[0])
	}
	if blocks[1].Name != "note" || blocks[1].Tokens != 1 {
		t.Fatalf("second block = %+v, want note/1", blocks[1])
	}
}

func TestBudgetBreakdownTiesBreakByName(t *testing.T) {
	blocks := BudgetBreakdown("", "", []string{"b-file", "a-file"})
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2: %+v", len(blocks), blocks)
	}
	if blocks[0].Name != "a-file" || blocks[1].Name != "b-file" {
		t.Fatalf("tie order = [%q %q], want [a-file b-file]", blocks[0].Name, blocks[1].Name)
	}
	if blocks[0].Tokens != blocks[1].Tokens {
		t.Fatalf("tied blocks have different token counts: %d vs %d", blocks[0].Tokens, blocks[1].Tokens)
	}
}

func TestDominantBlockRequiresStrictMajority(t *testing.T) {
	blocks := BudgetBreakdown("", "", []string{
		strings.Repeat("a", 400), // 100 -> 50%
		strings.Repeat("b", 400), // 100 -> 50%
	})
	if _, ok := DominantBlock(blocks); ok {
		t.Fatal("exactly 50% must not be reported as dominant")
	}

	single := BudgetBreakdown(strings.Repeat("x", 4), "", nil)
	got, ok := DominantBlock(single)
	if !ok || got.Name != BudgetBlockSystem || math.Abs(got.Percent-100) > 1e-9 {
		t.Fatalf("single block dominant = %+v, %v; want system at 100%%", got, ok)
	}
}

func TestDominantBlockFindsLargestRegardlessOfOrder(t *testing.T) {
	// Percent values are supplied explicitly; DominantBlock must scan for the
	// largest block instead of trusting slice order.
	blocks := []BudgetBlock{
		{Name: "small", Tokens: 10, Percent: 10},
		{Name: "big", Tokens: 60, Percent: 60},
		{Name: "mid", Tokens: 30, Percent: 30},
	}
	got, ok := DominantBlock(blocks)
	if !ok || got.Name != "big" {
		t.Fatalf("dominant = %+v, %v; want big", got, ok)
	}

	if _, ok := DominantBlock(nil); ok {
		t.Fatal("empty breakdown must not yield a dominant block")
	}
}

func TestDominantBlockComputesPercentWhenMissing(t *testing.T) {
	// Callers that only supply Tokens still get the >50% decision.
	blocks := []BudgetBlock{{Name: "a", Tokens: 60}, {Name: "b", Tokens: 40}}
	got, ok := DominantBlock(blocks)
	if !ok || got.Name != "a" {
		t.Fatalf("dominant = %+v, %v; want a", got, ok)
	}
}
