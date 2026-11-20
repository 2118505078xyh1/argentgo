package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// artifactNameRE is the externally observable filename contract: the spill is
// a plain ".log" file with a random hex id and no path separators or attacker
// controlled characters (safe filename + O_EXCL uniqueness).
var artifactNameRE = regexp.MustCompile(`^tool-output-[0-9a-f]{16}\.log$`)

func TestSpillLargeOutputSpillsTruncatesAndLinks(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("abcdefghij", 1000) // 10,000 bytes
	const threshold = 1024

	result, path, err := SpillLargeOutput(big, threshold, dir)
	if err != nil {
		t.Fatalf("SpillLargeOutput returned an error: %v", err)
	}
	if path == "" {
		t.Fatal("expected an artifact path for an oversized output")
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("artifact path %q is not under dir %q", path, dir)
	}
	if !artifactNameRE.MatchString(filepath.Base(path)) {
		t.Fatalf("unsafe artifact filename %q", filepath.Base(path))
	}

	// The full, unmodified output must be recoverable from the artifact.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(artifact): %v", err)
	}
	if string(data) != big {
		t.Fatal("artifact does not contain the full output")
	}

	// The returned body is a middle-elided head+tail view naming the artifact.
	if len(result) >= len(big) {
		t.Fatalf("result is %d bytes, not truncated from %d", len(result), len(big))
	}
	if !strings.HasPrefix(result, big[:64]) {
		t.Fatal("result lost the head window")
	}
	if !strings.HasSuffix(result, big[len(big)-64:]) {
		t.Fatal("result lost the tail window")
	}
	if !strings.Contains(result, "bytes elided") {
		t.Fatal("result is missing the elision marker")
	}
	if !strings.Contains(result, path) {
		t.Fatal("result is missing the recovery path")
	}
}

func TestSpillLargeOutputWriteFailureStillTruncates(t *testing.T) {
	base := t.TempDir()
	// A regular file used as the spill dir makes MkdirAll fail: the real write
	// failure path, not a simulated one.
	badDir := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(badDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	big := strings.Repeat("abcdefghij", 1000)
	const threshold = 1024

	result, path, err := SpillLargeOutput(big, threshold, badDir)
	if err != nil {
		t.Fatalf("write failure must not surface as an error, got %v", err)
	}
	if path != "" {
		t.Fatalf("expected no artifact path on write failure, got %q", path)
	}
	if len(result) >= len(big) {
		t.Fatal("write failure must still return a truncated result")
	}
	if !strings.Contains(result, "bytes elided") {
		t.Fatal("truncated result is missing the elision marker")
	}
	if strings.Contains(result, "full output:") {
		t.Fatal("failed spill must not advertise a recovery link")
	}
}

func TestSpillLargeOutputUnderThresholdUnchanged(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name  string
		text  string
		limit int
	}{
		{"short", "short output", 1024},
		{"exactly at threshold", strings.Repeat("x", 10), 10},
		{"empty", "", 10},
		{"disabled threshold", strings.Repeat("x", 1000), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, path, err := SpillLargeOutput(tc.text, tc.limit, dir)
			if err != nil {
				t.Fatalf("SpillLargeOutput returned an error: %v", err)
			}
			if path != "" {
				t.Fatalf("expected no artifact path, got %q", path)
			}
			if result != tc.text {
				t.Fatalf("result = %q, want unchanged %q", result, tc.text)
			}
		})
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("under-threshold calls created %d artifacts", len(entries))
	}
}

func TestSpillLargeOutputCJKAndEmojiRuneSafe(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("中文😀ab", 500) // CJK + 4-byte emoji + ASCII
	const threshold = 256

	result, path, err := SpillLargeOutput(big, threshold, dir)
	if err != nil {
		t.Fatalf("SpillLargeOutput returned an error: %v", err)
	}
	if path == "" {
		t.Fatal("expected an artifact path")
	}
	if !utf8.ValidString(result) {
		t.Fatal("spilled result is not valid UTF-8 (a rune was cut)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(artifact): %v", err)
	}
	if string(data) != big {
		t.Fatal("artifact does not contain the full output")
	}
	if len(result) >= len(big) {
		t.Fatal("result was not truncated")
	}

	// Sweep thresholds so the head/tail boundaries land inside CJK and emoji
	// runes; every produced result must remain valid UTF-8.
	for n := 2; n <= 80; n += 3 {
		_, _, err := SpillLargeOutput(big, n, dir)
		if err != nil {
			t.Fatalf("threshold %d: %v", n, err)
		}
	}
}

func TestSpillLargeOutputSkipsAlreadySpilledResult(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("abcdefghij", 1000)

	first, path1, err := SpillLargeOutput(big, 1024, dir)
	if err != nil || path1 == "" {
		t.Fatalf("first spill failed: path=%q err=%v", path1, err)
	}

	second, path2, err := SpillLargeOutput(first, 1024, dir)
	if err != nil {
		t.Fatalf("second SpillLargeOutput returned an error: %v", err)
	}
	if path2 != "" {
		t.Fatalf("second spill must not create an artifact, got %q", path2)
	}
	if second != first {
		t.Fatal("already-spilled output was modified on the second call")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("second spill created extra artifacts: %d total", len(entries))
	}
}
