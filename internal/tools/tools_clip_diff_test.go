package tools

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// This file adds differential / property tests for the stateless UTF-8 clip
// helpers in tools_clip.go. It reuses clipMarker, splitClipped and
// assertClipInvariants from tools_clip_utf8_test.go (same package) and never
// touches production code.

// reassembleChunks rebuilds text from consecutive byte chunks of the given
// size. A non-positive size means "whole input" (0-byte splitting is a no-op).
// Splitting on raw bytes intentionally cuts through multi-byte runes for
// sizes 1/2/3; reassembly must reproduce the input byte-for-byte.
func reassembleChunks(text string, chunkSize int) string {
	if chunkSize <= 0 {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); i += chunkSize {
		end := i + chunkSize
		if end > len(text) {
			end = len(text)
		}
		b.WriteString(text[i:end])
	}
	return b.String()
}

// TestClipToDeterministicAcrossInputChunking pins the "arbitrary chunking
// equivalence" property. clipTo is a pure, one-shot function with no state, so
// feeding it the same bytes regardless of how they were partitioned must yield
// an identical result. This guards against any future refactoring that makes
// the output depend on incremental / chunk-aligned state.
func TestClipToDeterministicAcrossInputChunking(t *testing.T) {
	texts := []struct {
		name string
		text string
	}{
		{"ascii", "abcdefghijklmnopqrstuvwxyz0123456789"},
		{"chinese", strings.Repeat("中文裁剪测试工具", 12)},
		{"emoji", strings.Repeat("😀🚀🎉", 10)},
		{"combining marks", strings.Repeat("e\u0301a\u0301", 12)},
		{"mixed scripts", strings.Repeat("ab中😀c\u0301", 9)},
	}
	limits := []int{1, 2, 7, 13, 25, 60, 200}
	// 0 represents the "whole input" chunking (the baseline below).
	chunkSizes := []int{0, 1, 2, 3, 7, 16}

	for _, tc := range texts {
		for _, limit := range limits {
			whole := clipTo(tc.text, limit)
			for _, size := range chunkSizes {
				t.Run(fmt.Sprintf("%s/limit=%d/chunk=%d", tc.name, limit, size), func(t *testing.T) {
					reassembled := reassembleChunks(tc.text, size)
					if reassembled != tc.text {
						t.Fatalf("reassembly changed the input: got %q, want %q", reassembled, tc.text)
					}
					if got := clipTo(reassembled, limit); got != whole {
						t.Fatalf("clipTo on %d-byte chunks differs from the whole-input call:\n got: %q\nwant: %q", size, got, whole)
					}
				})
			}
		}
	}
}

// naiveClip is an independent, rune-granular reference implementation of
// clipTo. Instead of byte-index arithmetic (head = limit*2/3, then backing off
// to a rune start), it accumulates whole runes until the 2/3 head and 1/3 tail
// byte budgets are exhausted, then joins them with the fixed marker. A correct
// clipTo must agree with it exactly; a mid-rune or off-by-one boundary bug will
// show up as a mismatch. It returns the retained head/tail separately so the
// caller can ignore the fixed marker overhead, mirroring splitClipped.
func naiveClip(text string, limit int) (head, tail string, truncated bool) {
	budget := limit
	if budget <= 0 {
		budget = maxToolOutput // clipTo treats non-positive limits as the default.
	}
	if len(text) <= budget {
		return text, "", false
	}
	headBudget := budget * 2 / 3
	tailBudget := budget - headBudget

	// Head: the largest rune-aligned prefix that fits in headBudget bytes.
	var hb strings.Builder
	used := 0
	for _, r := range text {
		n := utf8.RuneLen(r)
		if used+n > headBudget {
			break
		}
		hb.WriteRune(r)
		used += n
	}

	// Tail: the largest rune-aligned suffix that fits in tailBudget bytes.
	runes := []rune(text)
	var tailRunes []rune
	used = 0
	for i := len(runes) - 1; i >= 0; i-- {
		n := utf8.RuneLen(runes[i])
		if used+n > tailBudget {
			break
		}
		tailRunes = append(tailRunes, runes[i])
		used += n
	}
	for i, j := 0, len(tailRunes)-1; i < j; i, j = i+1, j-1 {
		tailRunes[i], tailRunes[j] = tailRunes[j], tailRunes[i]
	}
	return hb.String(), string(tailRunes), true
}

// clipAtoms are the syllables used to build random UTF-8 text: ASCII, Chinese,
// four-byte emoji and combining marks (including a standalone mark).
var clipAtoms = []string{
	"a", "b", "z", "0", "9", " ", "_",
	"中", "文", "测", "试", "工", "具",
	"😀", "🚀", "🎉", "👍",
	"e\u0301", "a\u0301", "\u0301",
}

func randomClipText(rng *rand.Rand, maxAtoms int) string {
	n := rng.Intn(maxAtoms) + 1
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(clipAtoms[rng.Intn(len(clipAtoms))])
	}
	return b.String()
}

// randomClipLimit deliberately mixes small limits, limits around the text
// length, non-positive limits (default budget) and limits that skip truncation
// entirely, so both the truncated and unchanged branches are exercised.
func randomClipLimit(rng *rand.Rand, textLen int) int {
	switch rng.Intn(5) {
	case 0:
		return rng.Intn(textLen + 1)
	case 1:
		return rng.Intn(24) + 1
	case 2:
		return textLen/2 + rng.Intn(10)
	case 3:
		return rng.Intn(10) - 5 // negative .. 4
	default:
		return textLen + rng.Intn(20) // no truncation expected
	}
}

// TestClipToMatchesRuneReferenceOnRandomText is a fixed-seed differential test:
// clipTo is compared against naiveClip on randomized mixed-script UTF-8 text
// and limits. Besides the shared invariants (valid UTF-8, rune/byte budget,
// constant marker overhead), the retained head and tail content must match the
// rune-granular reference byte-for-byte.
func TestClipToMatchesRuneReferenceOnRandomText(t *testing.T) {
	const seed = 20261120
	const iterations = 5000
	rng := rand.New(rand.NewSource(seed))

	for i := 0; i < iterations; i++ {
		text := randomClipText(rng, 120)
		limit := randomClipLimit(rng, len(text))
		got := clipTo(text, limit)

		assertClipInvariants(t, text, limit, got)

		refHead, refTail, refTrunc := naiveClip(text, limit)
		head, tail, trunc := splitClipped(t, got)
		if trunc != refTrunc {
			t.Fatalf("iteration %d: truncation mismatch: got %v, reference %v (limit=%d, text=%q, got=%q)",
				i, trunc, refTrunc, limit, text, got)
		}
		if !trunc {
			if got != text {
				t.Fatalf("iteration %d: untruncated result changed the text (limit=%d): got %q, want %q",
					i, limit, got, text)
			}
			continue
		}
		if head != refHead || tail != refTail {
			t.Fatalf("iteration %d: retained content differs from rune reference (limit=%d):\n head got:  %q\n head want: %q\n tail got:  %q\n tail want: %q\n text: %q",
				i, limit, head, refHead, tail, refTail, text)
		}
	}
}

// TestClipToDefaultLimitMatchesRuneReference covers the non-positive-limit
// branch with a text large enough to actually exceed maxToolOutput, which the
// short random texts cannot reach.
func TestClipToDefaultLimitMatchesRuneReference(t *testing.T) {
	text := strings.Repeat("😀中ab\u0301", 6000) // well over maxToolOutput bytes
	for _, limit := range []int{0, -1} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			got := clipTo(text, limit)
			assertClipInvariants(t, text, limit, got)

			refHead, refTail, refTrunc := naiveClip(text, limit)
			head, tail, trunc := splitClipped(t, got)
			if !trunc || !refTrunc {
				t.Fatalf("clipTo(..., %d) on a %d-byte text should truncate", limit, len(text))
			}
			if head != refHead || tail != refTail {
				t.Fatalf("clipTo(..., %d) content differs from rune reference:\n head got:  %q\n head want: %q\n tail got:  %q\n tail want: %q",
					limit, head, refHead, tail, refTail)
			}
		})
	}
}

// benchClipResult keeps benchmark results observable to the compiler.
var benchClipResult string

// BenchmarkClipToLargeInput records a pure performance baseline for clipTo on
// large ASCII, Chinese, emoji and mixed inputs. It intentionally sets no
// wall-clock threshold; it exists only so regressions can be spotted by
// comparing successive runs.
func BenchmarkClipToLargeInput(b *testing.B) {
	cases := []struct {
		name  string
		text  string
		limit int
	}{
		{"ascii-1KB", strings.Repeat("abcdefghij", 100), 500},
		{"chinese-150KB", strings.Repeat("中文裁剪测试工具", 6250), 20000},
		{"emoji-200KB", strings.Repeat("😀🚀", 25000), 30000},
		{"mixed-180KB", strings.Repeat("ab中😀e\u0301", 15000), 40000},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				benchClipResult = clipTo(bc.text, bc.limit)
			}
		})
	}
}
