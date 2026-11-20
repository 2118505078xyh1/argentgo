package tools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// writeRangeArtifact drops a file with a canonical spill name into dir and
// returns its path. The fixed id is fine here: these tests only read.
func writeRangeArtifact(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "tool-output-0123456789abcdef.log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadSpillRangeInclusiveAndClamped(t *testing.T) {
	dir := t.TempDir()
	path := writeRangeArtifact(t, dir, "l1\nl2\nl3\nl4\nl5\n")

	for _, tc := range []struct {
		name       string
		start, end int
		want       string
		wantTotal  int
	}{
		{"middle range is inclusive", 2, 4, "l2\nl3\nl4", 5},
		{"single line", 3, 3, "l3", 5},
		{"first line", 1, 1, "l1", 5},
		{"start clamped up to one", 0, 2, "l1\nl2", 5},
		{"negative start clamped up to one", -5, 1, "l1", 5},
		{"end clamped to total", 4, 999, "l4\nl5", 5},
		{"huge end clamped to total", 1, 1 << 40, "l1\nl2\nl3\nl4\nl5", 5},
		{"full bounded range has no trailing newline", 1, 5, "l1\nl2\nl3\nl4\nl5", 5},
		{"end zero reads to EOF with trailing newline", 2, 0, "l2\nl3\nl4\nl5\n", 5},
		{"negative end reads to EOF", 1, -1, "l1\nl2\nl3\nl4\nl5\n", 5},
		{"start past total is empty", 6, 10, "", 5},
		{"huge start is empty", 1 << 40, 0, "", 5},
		{"reversed range is empty", 4, 2, "", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := ReadSpillRange(path, tc.start, tc.end)
			if err != nil {
				t.Fatalf("ReadSpillRange(%d, %d): %v", tc.start, tc.end, err)
			}
			if got != tc.want {
				t.Fatalf("content = %q, want %q", got, tc.want)
			}
			if total != tc.wantTotal {
				t.Fatalf("totalLines = %d, want %d", total, tc.wantTotal)
			}
		})
	}
}

func TestReadSpillRangeTotalLinesWithAndWithoutTrailingNewline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		total   int
	}{
		{"with trailing newline", "a\nb\nc\n", 3},
		{"without trailing newline", "a\nb\nc", 3},
		{"single line no newline", "only", 1},
		{"single line with newline", "only\n", 1},
		{"blank separator counted", "a\n\nc\n", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeRangeArtifact(t, t.TempDir(), tc.content)
			_, total, err := ReadSpillRange(path, 1, 0)
			if err != nil {
				t.Fatal(err)
			}
			if total != tc.total {
				t.Fatalf("totalLines = %d, want %d", total, tc.total)
			}
		})
	}
}

func TestReadSpillRangeEmptyArtifact(t *testing.T) {
	path := writeRangeArtifact(t, t.TempDir(), "")
	got, total, err := ReadSpillRange(path, 1, 0)
	if err != nil {
		t.Fatalf("empty artifact: %v", err)
	}
	if got != "" || total != 0 {
		t.Fatalf("empty artifact = (%q, %d), want (\"\", 0)", got, total)
	}
}

func TestReadSpillRangeCJKAndEmojiRuneSafe(t *testing.T) {
	content := "中文😀第一行\nsecond 世界🌍line\n再见👋third\n末行 End 行\n"
	path := writeRangeArtifact(t, t.TempDir(), content)

	got, total, err := ReadSpillRange(path, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := "second 世界🌍line\n再见👋third"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if total != 4 {
		t.Fatalf("totalLines = %d, want 4", total)
	}
	if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("range contains broken runes: %q", got)
	}

	// A to-EOF read must also stay rune-safe and keep the trailing newline.
	full, total, err := ReadSpillRange(path, 1, 0)
	if err != nil || total != 4 || full != content {
		t.Fatalf("full read = (%q, %d, %v), want (%q, 4, nil)", full, total, err, content)
	}
}

func TestReadSpillRangeRejectsEscapingAndNonArtifactPaths(t *testing.T) {
	dir := t.TempDir()
	artifact := writeRangeArtifact(t, dir, "ok\n")

	// An artifact-shaped file with a traversal segment must be rejected even
	// though it resolves back to a real file in the spill dir.
	traversal := filepath.Join(dir, "..", filepath.Base(dir), "tool-output-0123456789abcdef.log")

	// A plain non-artifact name inside the spill dir must be rejected too.
	notes := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notes, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A symlink with a canonical artifact name pointing at an outside file.
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "tool-output-fedcba9876543210.log")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	missing := filepath.Join(dir, "tool-output-0000000000000000.log")

	for _, tc := range []struct {
		name string
		path string
		want error
	}{
		{"traversal segment", traversal, errSpillInvalidPath},
		{"non artifact filename", notes, errSpillNotArtifact},
		{"directory as artifact", dir, errSpillNotArtifact},
		{"symlink artifact", link, errSpillInvalidPath},
		{"missing artifact", missing, nil}, // os.Stat failure -> wrapped error
		{"empty path", "", errSpillInvalidPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ReadSpillRange(tc.path, 1, 1)
			if err == nil {
				t.Fatalf("ReadSpillRange(%q) unexpectedly succeeded", tc.path)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, tc.want)
			}
		})
	}

	// Sanity: the legitimate artifact in the same dir still reads fine.
	if got, total, err := ReadSpillRange(artifact, 1, 1); err != nil || got != "ok" || total != 1 {
		t.Fatalf("legitimate artifact = (%q, %d, %v)", got, total, err)
	}
}

func TestReadSpillRangeCorruptUTF8ArtifactRejected(t *testing.T) {
	path := writeRangeArtifact(t, t.TempDir(), "ok\n")
	if err := os.WriteFile(path, []byte{0xff, 0xfe, 0x80}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadSpillRange(path, 1, 1); err == nil {
		t.Fatal("invalid UTF-8 artifact was accepted")
	}
}

func TestReadSpillRangeRejectsPathWithDotDotSuffix(t *testing.T) {
	// A trailing ".." segment is an escape even without a filename after it.
	// Build the string directly: filepath.Join would clean the segment away.
	path := t.TempDir() + string(filepath.Separator) + ".."
	if _, _, err := ReadSpillRange(path, 1, 1); !errors.Is(err, errSpillInvalidPath) {
		t.Fatalf("error = %v, want errSpillInvalidPath", err)
	}
}
