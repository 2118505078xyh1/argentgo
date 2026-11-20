package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// The hashline feature is opt-in: with hashline=false (or absent) every error
// string and the repair ladder must stay byte-identical to the pre-feature
// tool, because the eval goldens assert on those exact bytes.
func TestHashlineFalsePreservesDefaultErrorStrings(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "x.txt", "dup\ndup\n")

	_, err := runToolArgs(t, root, "edit", map[string]any{
		"path": "x.txt", "old": "dup", "new": "done", "hashline": false,
	})
	want := "edit: old text appears 2 times in x.txt (not unique); a unique match is required"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want exactly %q", err, want)
	}

	_, err = runToolArgs(t, root, "edit", map[string]any{
		"path": "x.txt", "old": "missing", "new": "done", "hashline": false,
	})
	want = "edit: old text not found in x.txt"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want exactly %q", err, want)
	}
	if content := readFixture(t, path); content != "dup\ndup\n" {
		t.Fatalf("failed edits changed the file: %q", content)
	}
}

func TestHashlineFalseKeepsRepairPathUnchanged(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "x.txt", "needle one\nneedle two\n")

	_, err := runToolArgs(t, root, "edit", map[string]any{
		"path": "x.txt", "old": "needle wan", "new": "X", "repair": true, "hashline": false,
	})
	if err == nil {
		t.Fatal("expected the pre-existing repair diagnostic")
	}
	msg := err.Error()
	for _, want := range []string{
		"edit repair failed",
		"after 1 edit(s) attempted",
		"file unchanged",
		"most similar lines:",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic %q missing %q", msg, want)
		}
	}
	if content := readFixture(t, path); content != "needle one\nneedle two\n" {
		t.Fatalf("failed edit changed the file: %q", content)
	}
}

func TestHashlineCorrectAnchorAndHashAppliesEdit(t *testing.T) {
	root := t.TempDir()
	anchor := "func add(a, b int) int {\n\treturn a + b\n}"
	path := writeFixture(t, root, "calc.go", "package calc\n\n"+anchor+"\n")

	// No `old` is supplied: the anchor itself is the original text.
	_, err := runToolArgs(t, root, "edit", map[string]any{
		"path":        "calc.go",
		"hashline":    true,
		"anchor":      anchor,
		"anchor_hash": hashlineHash(anchor),
		"new":         "func add(a, b int) int {\n\treturn a - b\n}",
	})
	if err != nil {
		t.Fatalf("edit with valid hashline anchor: %v", err)
	}
	want := "package calc\n\nfunc add(a, b int) int {\n\treturn a - b\n}\n"
	if content := readFixture(t, path); content != want {
		t.Fatalf("content = %q, want %q", content, want)
	}
}

func TestHashlineWrongAnchorHashReturnsStaleError(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "f.txt", "alpha\nbeta\ngamma\n")

	_, err := runToolArgs(t, root, "edit", map[string]any{
		"path":        "f.txt",
		"hashline":    true,
		"anchor":      "beta",
		"anchor_hash": "000000000000",
		"new":         "BETA",
	})
	if err == nil {
		t.Fatal("expected a stale anchor error for a mismatched hash")
	}
	msg := err.Error()
	for _, want := range []string{"stale anchor", "content changed since it was read", "expected hash", "got"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
	if content := readFixture(t, path); content != "alpha\nbeta\ngamma\n" {
		t.Fatalf("stale edit changed the file: %q", content)
	}
}

func TestHashlineRecoversWhenAnchorShifted(t *testing.T) {
	root := t.TempDir()
	anchor := "gamma\ndelta"
	// The model read "gamma\ndelta" at line 3. Lines were inserted above it;
	// the anchor content is intact, so a content match must find it at its new
	// location without relying on the old line number.
	path := writeFixture(t, root, "f.txt", "alpha\nbeta\n"+anchor+"\nepsilon\n")

	_, err := runToolArgs(t, root, "edit", map[string]any{
		"path":        "f.txt",
		"hashline":    true,
		"anchor":      anchor,
		"anchor_hash": hashlineHash(anchor),
		"new":         "GAMMA\nDELTA",
	})
	if err != nil {
		t.Fatalf("edit with shifted anchor: %v", err)
	}
	want := "alpha\nbeta\nGAMMA\nDELTA\nepsilon\n"
	if content := readFixture(t, path); content != want {
		t.Fatalf("content = %q, want %q", content, want)
	}
}

func TestHashlineChangedAnchorReturnsClearStaleError(t *testing.T) {
	root := t.TempDir()
	// What the model saw and hashed. The file now holds "new delta" instead,
	// so neither a literal match nor the repair ladder can recover the anchor.
	saw := "gamma\nold delta"
	content := "alpha\nbeta\ngamma\nnew delta\nepsilon\n"
	path := writeFixture(t, root, "f.txt", content)

	_, err := runToolArgs(t, root, "edit", map[string]any{
		"path":        "f.txt",
		"hashline":    true,
		"anchor":      saw,
		"anchor_hash": hashlineHash(saw),
		"new":         "X",
	})
	if err == nil {
		t.Fatal("expected a clear stale-anchor error when the content genuinely changed")
	}
	msg := err.Error()
	for _, want := range []string{"stale anchor no longer present", "file unchanged", "most similar lines:"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
	if got := readFixture(t, path); got != content {
		t.Fatalf("stale edit changed the file: %q", got)
	}
}

func TestHashlineWithoutAnchorUsesExactBehavior(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "f.txt", "alpha\nbeta\n")

	_, err := runToolArgs(t, root, "edit", map[string]any{
		"path": "f.txt", "hashline": true, "old": "beta", "new": "BETA",
	})
	if err != nil {
		t.Fatalf("hashline=true with no anchor should use the exact path: %v", err)
	}
	if content := readFixture(t, path); content != "alpha\nBETA\n" {
		t.Fatalf("content = %q, want %q", content, "alpha\nBETA\n")
	}
}

func TestHashlineHashStableFormatAndContentSensitive(t *testing.T) {
	h := hashlineHash("hello")
	// sha256("hello") = 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824
	if want := "2cf24dba5fb0"; h != want {
		t.Fatalf("hashlineHash(%q) = %q, want %q", "hello", h, want)
	}
	if len(h) != 12 {
		t.Fatalf("hashlineHash length = %d, want 12", len(h))
	}
	for _, r := range h {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("hashlineHash returned a non-hex character %q in %q", r, h)
		}
	}
	if again := hashlineHash("hello"); again != h {
		t.Fatalf("hashlineHash is not stable: %q then %q", h, again)
	}
	if hashlineHash("hello") == hashlineHash("hello!") {
		t.Fatal("different content produced the same hash")
	}
}

func TestEditSchemaAdvertisesHashlineFields(t *testing.T) {
	schema := editTool{}.Schema()
	if !json.Valid([]byte(schema)) {
		t.Fatalf("edit schema is not valid JSON: %s", schema)
	}
	if len(schema) >= 1024 {
		t.Fatalf("edit schema is %d bytes, want < 1024", len(schema))
	}

	var parsed struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
		t.Fatalf("cannot decode edit schema: %v", err)
	}
	for name, typ := range map[string]string{
		"anchor": "string", "anchor_hash": "string", "hashline": "boolean",
	} {
		prop, ok := parsed.Properties[name]
		if !ok {
			t.Fatalf("edit schema does not advertise %q", name)
		}
		if prop.Type != typ {
			t.Fatalf("%q type = %q, want %q", name, prop.Type, typ)
		}
	}
	if desc := parsed.Properties["anchor_hash"].Description; !strings.Contains(desc, "12") || !strings.Contains(desc, "sha256") {
		t.Fatalf("anchor_hash description does not state the hash contract: %q", desc)
	}

	found := false
	for _, r := range parsed.Required {
		if r == "path" {
			found = true
		}
	}
	if !found {
		t.Fatalf("edit schema no longer requires path: %v", parsed.Required)
	}
}
