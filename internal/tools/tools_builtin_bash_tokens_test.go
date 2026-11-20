package tools

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

const bashHintPrefix = "[output: ~"

// TestClipBashOutputAddsHintWhenClipped verifies that an oversized command
// output gets a short volume hint at the head, without disturbing the clipped
// body or the truncation marker.
func TestClipBashOutputAddsHintWhenClipped(t *testing.T) {
	ctx := context.WithValue(context.Background(), outputLimitKey{}, 512)
	raw := strings.Repeat("abcdefgh", 512) // 4096 bytes, well over the 512-byte cap
	got := clipBashOutput(ctx, raw)

	if !strings.HasPrefix(got, bashHintPrefix) {
		t.Fatalf("clipped output has no volume hint at the head: %q", got[:min(80, len(got))])
	}
	firstLine, _, _ := strings.Cut(got, "\n")
	if !strings.Contains(firstLine, "k tokens, clipped]") {
		t.Fatalf("hint has an unexpected format: %q", firstLine)
	}
	if !strings.Contains(got, "[truncated]") {
		t.Fatalf("output was not actually clipped: %q", got[:min(80, len(got))])
	}
}

// TestClipBashOutputUsesDefaultLimitHint covers the no-limit-in-context branch:
// output beyond maxToolOutput must still be hinted.
func TestClipBashOutputUsesDefaultLimitHint(t *testing.T) {
	raw := strings.Repeat("x", maxToolOutput+100)
	got := clipBashOutput(context.Background(), raw)
	if !strings.HasPrefix(got, bashHintPrefix) {
		t.Fatalf("output over the default limit has no hint: %q", got[:min(80, len(got))])
	}
}

// TestClipBashOutputNoHintWhenSmall verifies that small output is returned
// byte-for-byte unchanged, with no volume hint.
func TestClipBashOutputNoHintWhenSmall(t *testing.T) {
	ctx := context.WithValue(context.Background(), outputLimitKey{}, 512)
	const raw = "short output\n"
	got := clipBashOutput(ctx, raw)
	if got != raw {
		t.Fatalf("small output changed: got %q, want %q", got, raw)
	}
	if strings.Contains(got, bashHintPrefix) {
		t.Fatalf("small output got an unexpected hint: %q", got)
	}
}

// TestBashOutputTokensCoversWideAndNarrowRunes verifies the lightweight estimate:
// CJK runes count as one token each, other runes follow chars/4 (rounded up).
func TestBashOutputTokensCoversWideAndNarrowRunes(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"ascii multiple of four", strings.Repeat("a", 400), 100},
		{"ascii rounded up", strings.Repeat("a", 5), 2},
		{"han", strings.Repeat("中", 100), 100},
		{"kana and hangul", "アイウ한글", 5},
		{"mixed", "中文测试", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bashOutputTokens(tc.text); got != tc.want {
				t.Fatalf("bashOutputTokens(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

// TestClipBashOutputHintUsesCJKEstimate verifies the hint reflects the CJK-aware
// estimate rather than a naive byte/4 or rune/4 count.
func TestClipBashOutputHintUsesCJKEstimate(t *testing.T) {
	ctx := context.WithValue(context.Background(), outputLimitKey{}, 256)
	raw := strings.Repeat("中", 2000) // 2000 wide runes => ~2.0k tokens
	got := clipBashOutput(ctx, raw)
	firstLine, _, _ := strings.Cut(got, "\n")
	const want = "[output: ~2.0k tokens, clipped]"
	if firstLine != want {
		t.Fatalf("hint = %q, want %q", firstLine, want)
	}
}

// TestBashClippedOutputPreservesExitStatus verifies the text-only enhancement
// did not change exit-status handling: a non-zero exit still yields an error,
// and the (clipped) output still carries the hint.
func TestBashClippedOutputPreservesExitStatus(t *testing.T) {
	path, _ := shellInvocation("")
	if path == "cmd" {
		t.Skip("portable status command requires a POSIX shell")
	}
	// Some Windows installs expose a WindowsApps bash.exe stub that is not a
	// usable shell; the tool cannot run any command through it.
	if resolved, err := exec.LookPath(path); err == nil && strings.Contains(resolved, "WindowsApps") {
		t.Skip("bash resolves to the WindowsApps stub, not a real shell")
	}
	command := `i=0; while [ "$i" -lt 3000 ]; do printf '%040d\n' 0; i=$((i+1)); done; exit 7`
	rawArgs, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}

	out, err := (bashTool{}).Run(context.Background(), t.TempDir(), rawArgs)
	if err == nil {
		t.Fatal("non-zero exit must still produce an error")
	}
	if !strings.Contains(err.Error(), "7") {
		t.Fatalf("error lost the exit status: %q", err.Error())
	}
	if !strings.HasPrefix(out, bashHintPrefix) {
		t.Fatalf("clipped output has no volume hint: %q", out[:min(80, len(out))])
	}
}
