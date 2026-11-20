package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBudgetTierMilestones(t *testing.T) {
	tests := []struct {
		name   string
		used   int
		window int
		want   int
	}{
		{"zero used", 0, 1000, 0},
		{"negative used", -5, 1000, 0},
		{"zero window", 500, 0, 0},
		{"negative window", 500, -100, 0},
		{"both non-positive", -1, -1, 0},
		{"below half", 499, 1000, 0},
		{"exactly half", 500, 1000, 1},
		{"between half and three quarters", 749, 1000, 1},
		{"exactly three quarters", 750, 1000, 2},
		{"between three quarters and ninety percent", 899, 1000, 2},
		{"exactly ninety percent", 900, 1000, 3},
		{"between ninety and ninety-five percent", 949, 1000, 3},
		{"exactly ninety-five percent", 950, 1000, 4},
		{"above ninety-five percent", 960, 1000, 4},
		{"full window", 1000, 1000, 4},
		{"over window", 1200, 1000, 4},
		{"large window half", 100000, 200000, 1},
		{"large window three quarters", 150000, 200000, 2},
		{"large window ninety percent", 180000, 200000, 3},
		{"large window ninety-five percent", 190000, 200000, 4},
		{"non divisible window", 2, 3, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BudgetTier(tt.used, tt.window); got != tt.want {
				t.Fatalf("BudgetTier(%d, %d) = %d, want %d", tt.used, tt.window, got, tt.want)
			}
		})
	}
}

func TestShouldRemindOnTierCrossOnly(t *testing.T) {
	delivered := map[int]bool{}
	if !ShouldRemind(0, 1, delivered, false) {
		t.Fatal("crossing 0 -> 1 should remind")
	}
	if !delivered[1] {
		t.Fatal("tier 1 should be recorded as delivered")
	}

	// Same tier, next call: silent even though the tier is not delivered yet.
	flat := map[int]bool{}
	if ShouldRemind(2, 2, flat, false) {
		t.Fatal("flat tier should not remind")
	}
	if len(flat) != 0 {
		t.Fatalf("flat tier should not record delivery, got %v", flat)
	}

	// A shrinking window is silent, not a warning.
	if ShouldRemind(3, 1, map[int]bool{}, false) {
		t.Fatal("falling tier should not remind")
	}

	// Jumping several tiers still produces exactly one reminder, for the tier
	// actually reached.
	jump := map[int]bool{}
	if !ShouldRemind(0, 3, jump, false) {
		t.Fatal("crossing 0 -> 3 should remind")
	}
	if !jump[3] || len(jump) != 1 {
		t.Fatalf("jump should record only tier 3, got %v", jump)
	}
}

func TestShouldRemindDeduplicatesWithinWindow(t *testing.T) {
	delivered := map[int]bool{2: true}
	if ShouldRemind(1, 2, delivered, false) {
		t.Fatal("an already-delivered tier must not repeat")
	}
	if !ShouldRemind(2, 3, delivered, false) {
		t.Fatal("a new higher tier should remind")
	}
	if !delivered[3] {
		t.Fatalf("tier 3 should be recorded, got %v", delivered)
	}

	// Descend then climb back to an announced tier: still deduplicated.
	if ShouldRemind(3, 1, delivered, false) {
		t.Fatal("falling tier should not remind")
	}
	if ShouldRemind(1, 2, delivered, false) {
		t.Fatal("tier 2 must stay deduplicated after a dip")
	}
}

func TestShouldRemindNewWindowResetsAndWarns(t *testing.T) {
	delivered := map[int]bool{1: true, 2: true, 3: true}
	if !ShouldRemind(3, 1, delivered, true) {
		t.Fatal("a new window should remind")
	}
	if len(delivered) != 1 || !delivered[1] {
		t.Fatalf("new window should clear old deliveries and record only the current tier, got %v", delivered)
	}
	// The freshly delivered tier is deduplicated without another reset.
	if ShouldRemind(1, 1, delivered, false) {
		t.Fatal("tier 1 should not repeat in the same new window")
	}

	// A new window also warns at tier 0 (fresh window, nothing consumed).
	fresh := map[int]bool{}
	if !ShouldRemind(4, 0, fresh, true) {
		t.Fatal("a new window should warn even at tier 0")
	}
	if len(fresh) != 1 || !fresh[0] {
		t.Fatalf("new window at tier 0 should record only tier 0, got %v", fresh)
	}
}

func TestShouldRemindNilDeliveredIsSafe(t *testing.T) {
	if !ShouldRemind(0, 1, nil, false) {
		t.Fatal("crossing with a nil delivered map should still decide to remind")
	}
	if !ShouldRemind(1, 1, nil, true) {
		t.Fatal("new window with a nil delivered map should still decide to remind")
	}
}

func TestBudgetReminderTextContentAndStability(t *testing.T) {
	const want = "You have approximately 50k tokens left in this context window; consider wrapping up or summarizing."
	got := BudgetReminderText(50000, 200000)
	if got != want {
		t.Fatalf("BudgetReminderText(50000, 200000) = %q, want %q", got, want)
	}
	if again := BudgetReminderText(50000, 200000); again != got {
		t.Fatalf("text is not stable: %q then %q", got, again)
	}
	// The instruction is English and locale-independent: no non-ASCII bytes.
	if !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8 in %q", got)
	}
	for _, r := range got {
		if r > utf8.RuneSelf {
			t.Fatalf("reminder text must be ASCII-only, found %q in %q", r, got)
		}
	}
	for _, phrase := range []string{"You have approximately", "tokens left in this context window", "consider wrapping up or summarizing"} {
		if !strings.Contains(got, phrase) {
			t.Fatalf("text %q missing phrase %q", got, phrase)
		}
	}
}

func TestBudgetReminderTextClampsAndFormatsK(t *testing.T) {
	tests := []struct {
		name      string
		remaining int
		wantNum   string
	}{
		{"negative clamps to zero", -100, "0k"},
		{"zero", 0, "0k"},
		{"fifty", 50, "0.1k"},
		{"five hundred", 500, "0.5k"},
		{"rounds up to one k", 999, "1k"},
		{"rounds nine fifty to one k", 950, "1k"},
		{"one thousand", 1000, "1k"},
		{"one thousand two hundred thirty four", 1234, "1.2k"},
		{"fifty thousand", 50000, "50k"},
		{"large rounded value", 1234567, "1234.6k"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BudgetReminderText(tt.remaining, 100000)
			wantPhrase := "approximately " + tt.wantNum + " tokens"
			if !strings.Contains(got, wantPhrase) {
				t.Fatalf("BudgetReminderText(%d) = %q, want phrase %q", tt.remaining, got, wantPhrase)
			}
			if strings.Contains(got, "-") {
				t.Fatalf("BudgetReminderText(%d) leaked a negative sign: %q", tt.remaining, got)
			}
		})
	}
}

func TestBudgetReminderTextIgnoresWindowAndCJKLocale(t *testing.T) {
	base := BudgetReminderText(10000, 100000)
	for _, window := range []int{0, -1, 200000, 12345} {
		if got := BudgetReminderText(10000, window); got != base {
			t.Fatalf("window %d changed the text: %q vs %q", window, got, base)
		}
	}
	// No CJK characters can appear regardless of call site or arguments.
	for _, r := range BudgetReminderText(-1, 0) {
		if r > utf8.RuneSelf {
			t.Fatalf("CJK/non-ASCII character in reminder text: %q", r)
		}
	}
}
