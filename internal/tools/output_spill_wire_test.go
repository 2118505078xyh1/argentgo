package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func spillArtifacts(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	var found []os.DirEntry
	for _, e := range entries {
		if artifactNameRE.MatchString(e.Name()) {
			found = append(found, e)
		}
	}
	return found
}

func TestSpillToolOutputDisabledByDefault(t *testing.T) {
	// No ctx config and no env switch: the feature must be completely inert.
	t.Setenv(spillDirEnv, "")
	t.Setenv(spillThresholdEnv, "")
	big := strings.Repeat("x", maxToolOutput+100)

	out, handled := spillToolOutput(context.Background(), big)
	if handled || out != "" {
		t.Fatalf("spillToolOutput handled output with no opt-in: out=%q handled=%v", out, handled)
	}

	got := clip(context.Background(), big)
	if strings.Contains(got, "bytes elided") {
		t.Fatalf("default clip produced a spill marker: %q", got[:min(len(got), 120)])
	}
	if !strings.Contains(got, "[truncated]") {
		t.Fatal("default clip did not use the ordinary truncation marker")
	}
	if len(got) >= len(big) {
		t.Fatal("default clip did not truncate")
	}
}

func TestClipSpillsOversizedOutputWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	ctx := WithOutputSpill(context.Background(), 1024, dir)
	big := strings.Repeat("0123456789", 500) // 5,000 bytes

	got := clip(ctx, big)
	if len(got) >= len(big) {
		t.Fatalf("result is %d bytes, want it truncated from %d", len(got), len(big))
	}
	if !strings.HasPrefix(got, big[:64]) || !strings.HasSuffix(got, big[len(big)-64:]) {
		t.Fatal("spilled result lost the head or tail window")
	}
	if !strings.Contains(got, "bytes elided") {
		t.Fatal("spilled result is missing the elision marker")
	}

	artifacts := spillArtifacts(t, dir)
	if len(artifacts) != 1 {
		t.Fatalf("got %d spill artifacts, want 1", len(artifacts))
	}
	path := filepath.Join(dir, artifacts[0].Name())
	if !strings.Contains(got, path) {
		t.Fatalf("result is missing the recovery path %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(artifact): %v", err)
	}
	if string(data) != big {
		t.Fatal("artifact does not contain the full output")
	}
}

func TestClipSpillKeepsOrdinaryOutputUnchanged(t *testing.T) {
	dir := t.TempDir()
	// The threshold sits above maxToolOutput so a merely-large output still
	// follows the old clipTo path rather than being spilled.
	ctx := WithOutputSpill(context.Background(), maxToolOutput+4096, dir)

	small := "a short, ordinary tool result"
	if got := clip(ctx, small); got != small {
		t.Fatalf("ordinary output changed: got %q, want %q", got, small)
	}

	medium := strings.Repeat("m", maxToolOutput+50)
	got := clip(ctx, medium)
	if strings.Contains(got, "bytes elided") {
		t.Fatal("output below the spill threshold was spilled")
	}
	if !strings.Contains(got, "[truncated]") {
		t.Fatal("output below the spill threshold was not clipped by the old path")
	}
	if n := len(spillArtifacts(t, dir)); n != 0 {
		t.Fatalf("created %d artifacts below the threshold", n)
	}
}

func TestClipSpillWriteFailureStillTruncates(t *testing.T) {
	base := t.TempDir()
	// A regular file used as the spill dir makes MkdirAll fail: the real write
	// failure path, exercised through clip() rather than the raw helper.
	badDir := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(badDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ctx := WithOutputSpill(context.Background(), 1024, badDir)
	big := strings.Repeat("z", 5000)

	got := clip(ctx, big)
	if len(got) >= len(big) {
		t.Fatal("write failure must still return a truncated result")
	}
	if !strings.Contains(got, "bytes elided") {
		t.Fatal("degraded result is missing the elision marker")
	}
	if strings.Contains(got, "full output:") {
		t.Fatal("failed spill must not advertise a recovery link")
	}
}

func TestClipSpillCJKNotCut(t *testing.T) {
	dir := t.TempDir()
	ctx := WithOutputSpill(context.Background(), 256, dir)
	big := strings.Repeat("中文😀ab", 200) // CJK + 4-byte emoji + ASCII

	got := clip(ctx, big)
	if !utf8.ValidString(got) {
		t.Fatal("spilled result is not valid UTF-8 (a rune was cut)")
	}
	// Sweep the threshold so head/tail boundaries land inside multibyte runes.
	for n := 2; n <= 80; n += 3 {
		out := clip(WithOutputSpill(context.Background(), n, dir), big)
		if !utf8.ValidString(out) {
			t.Fatalf("threshold %d produced invalid UTF-8", n)
		}
	}
	artifacts := spillArtifacts(t, dir)
	if len(artifacts) == 0 {
		t.Fatal("expected at least one artifact for oversized CJK output")
	}
	data, err := os.ReadFile(filepath.Join(dir, artifacts[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile(artifact): %v", err)
	}
	if string(data) != big {
		t.Fatal("artifact does not contain the full CJK output")
	}
}

func TestClipSecondSpillSkipped(t *testing.T) {
	dir := t.TempDir()
	ctx := WithOutputSpill(context.Background(), 1024, dir)
	big := strings.Repeat("abcdefghij", 500)

	first := clip(ctx, big)
	if !strings.Contains(first, "bytes elided") {
		t.Fatal("first clip did not spill the output")
	}
	second := clip(ctx, first)
	if second != first {
		t.Fatal("already-spilled output was modified by a second clip")
	}
	if n := len(spillArtifacts(t, dir)); n != 1 {
		t.Fatalf("second spill created extra artifacts: %d total", n)
	}
}

func TestClipSpillEnvOptIn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(spillDirEnv, dir)
	t.Setenv(spillThresholdEnv, "1024")
	big := strings.Repeat("q", 5000)

	got := clip(context.Background(), big)
	if !strings.Contains(got, "bytes elided") {
		t.Fatal("env opt-in did not spill the output")
	}
	if n := len(spillArtifacts(t, dir)); n != 1 {
		t.Fatalf("env opt-in created %d artifacts, want 1", n)
	}
}

func TestClipSpillEnvMissingThresholdDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(spillDirEnv, dir)
	t.Setenv(spillThresholdEnv, "")
	big := strings.Repeat("p", 5000)

	got := clip(context.Background(), big)
	if strings.Contains(got, "bytes elided") {
		t.Fatal("spilling enabled with a dir but no threshold")
	}
	if n := len(spillArtifacts(t, dir)); n != 0 {
		t.Fatalf("half-configured env switch created %d artifacts", n)
	}
}

func TestWithOutputSpillExplicitDisableOverridesEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(spillDirEnv, dir)
	t.Setenv(spillThresholdEnv, "1024")
	ctx := WithOutputSpill(context.Background(), 0, "") // explicit opt-out
	big := strings.Repeat("w", 5000)

	got := clip(ctx, big)
	if strings.Contains(got, "bytes elided") {
		t.Fatal("explicit disable did not override the env switch")
	}
	if n := len(spillArtifacts(t, dir)); n != 0 {
		t.Fatalf("explicit disable still created %d artifacts", n)
	}
}
