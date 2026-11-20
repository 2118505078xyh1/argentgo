package security_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/automation"
	"github.com/2118505078xyh1/argentgo/internal/commands"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/skills"
	"github.com/2118505078xyh1/argentgo/internal/workflow"
)

// testTag is a Unicode Tag Block letter: invisible in an editor or terminal.
const testTag = "\U000e0041"

// wantWarning asserts the single stable warning contract shared by all parsers.
func wantWarning(t *testing.T, warnings []string) {
	t.Helper()
	if len(warnings) != 1 || warnings[0] != security.ArtifactInvisibleWarning {
		t.Fatalf("warnings = %q, want exactly [%q]", warnings, security.ArtifactInvisibleWarning)
	}
}

func TestScanArtifactDetectsTagsAndBidi(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantTags bool
		wantBidi bool
	}{
		{"clean ascii", "echo hello", false, false},
		{"cjk and emoji", "日本語 🚀", false, false},
		{"below tag range", "\U000dffff", false, false},
		{"above tag range", "\U000e0080", false, false},
		{"lowest tag", "\U000e0000", true, false},
		{"highest tag", "\U000e007f", true, false},
		{"tag inside text", "echo " + testTag + "hi", true, false},
		{"bidi override", "a\u202eb", false, true},
		{"bom in middle", "a\ufeffb", false, true},
		{"leading bom ignored", "\ufeffecho hi", false, false},
		{"mixed", "a" + testTag + "\u202eb", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := security.ScanArtifact(tc.in)
			if got.HasTags != tc.wantTags || got.HasBidi != tc.wantBidi {
				t.Fatalf("ScanArtifact(%q) = %+v, want tags=%v bidi=%v", tc.in, got, tc.wantTags, tc.wantBidi)
			}
			if tc.wantTags || tc.wantBidi {
				wantWarning(t, got.Warnings())
			} else if got.Warning != "" || got.Warnings() != nil {
				t.Fatalf("clean content produced a warning: %q", got.Warning)
			}
		})
	}
}

func TestScanArtifactCoversEveryBidiRange(t *testing.T) {
	// Same runes as EscapeBidi: ALM, LRM, RLM, the embedding/override block,
	// the isolate block, and BOM. A leading BOM alone is deliberately ignored,
	// so prefix each one to keep the check about the rune itself.
	for _, r := range []rune{
		'\u061C', '\u200E', '\u200F',
		'\u202A', '\u202B', '\u202C', '\u202D', '\u202E',
		'\u2066', '\u2067', '\u2068', '\u2069', '\uFEFF',
	} {
		got := security.ScanArtifact("x" + string(r) + "y")
		if !got.HasBidi || got.HasTags {
			t.Fatalf("ScanArtifact(%q) = %+v, want bidi detected", string(r), got)
		}
		if !security.ContainsBidi(string(r)) {
			t.Fatalf("ContainsBidi(%q) = false", string(r))
		}
	}
}

func TestScanArtifactIsBounded(t *testing.T) {
	const capBytes = 1 << 20
	// A hidden tag ending exactly at the cap is still flagged (it is 4 bytes).
	within := strings.Repeat("a", capBytes-len(testTag)) + testTag
	if !security.ScanArtifact(within).HasTags {
		t.Fatal("tag within the scan cap was not detected")
	}
	// The same tag one byte past the cap is outside the bounded scan.
	beyond := strings.Repeat("a", capBytes) + testTag
	if got := security.ScanArtifact(beyond); got.HasTags || got.Warning != "" {
		t.Fatalf("content past the scan cap was flagged: %+v", got)
	}
}

func TestRedactArtifactContent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"clean text unchanged", "日本語 🚀\nhello", "日本語 🚀\nhello"},
		{"tags removed", "ignore " + testTag + "this", "ignore this"},
		{"bidi escaped", "a\u202eb", `a\u202eb`},
		{"mixed", "ignore " + testTag + "this \u2069now", `ignore this \u2069now`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := security.RedactArtifactContent(tc.in); got != tc.want {
				t.Fatalf("RedactArtifactContent(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// Redaction is bounded too: an oversized document is truncated to the cap.
	const capBytes = 1 << 20
	if got := len(security.RedactArtifactContent(strings.Repeat("a", capBytes+1))); got != capBytes {
		t.Fatalf("oversized redaction length = %d, want %d", got, capBytes)
	}
}

func TestWorkflowParseWarnsOnInvisibleContent(t *testing.T) {
	doc := `{"name":"review","steps":[{"name":"scan","prompt":"list ` + testTag + `files"}]}`
	def, err := workflow.ParseDefinition([]byte(doc))
	if err != nil {
		t.Fatalf("ParseDefinition: %v", err)
	}
	wantWarning(t, def.Warnings)
	if want := "list " + testTag + "files"; def.Steps[0].Prompt != want {
		t.Fatalf("prompt = %q, want unchanged %q", def.Steps[0].Prompt, want)
	}
	assertNoWarningsField(t, def)

	clean := `{"name":"review","steps":[{"name":"scan","prompt":"list files"}]}`
	cleanDef, err := workflow.ParseDefinition([]byte(clean))
	if err != nil {
		t.Fatalf("ParseDefinition(clean): %v", err)
	}
	if len(cleanDef.Warnings) != 0 {
		t.Fatalf("clean workflow warnings = %q", cleanDef.Warnings)
	}
}

func TestSkillsParseWarnsOnInvisibleContent(t *testing.T) {
	doc := "---\nname: review\ndescription: d\n---\nDo " + testTag + "this\n"
	skill, err := skills.Parse(doc, "SKILL.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wantWarning(t, skill.Warnings)
	if want := "Do " + testTag + "this"; skill.Body != want {
		t.Fatalf("body = %q, want unchanged %q", skill.Body, want)
	}

	clean := "---\nname: review\ndescription: d\n---\nDo this\n"
	cleanSkill, err := skills.Parse(clean, "SKILL.md")
	if err != nil {
		t.Fatalf("Parse(clean): %v", err)
	}
	if len(cleanSkill.Warnings) != 0 {
		t.Fatalf("clean skill warnings = %q", cleanSkill.Warnings)
	}
}

func TestCommandsParseWarnsOnInvisibleContent(t *testing.T) {
	// Frontmatter path.
	doc := "---\ndescription: d\n---\nDo " + testTag + "this\n"
	cmd, err := commands.ParseStrict(doc, "review.md")
	if err != nil {
		t.Fatalf("ParseStrict: %v", err)
	}
	wantWarning(t, cmd.Warnings)
	if want := "Do " + testTag + "this"; cmd.Body != want {
		t.Fatalf("body = %q, want unchanged %q", cmd.Body, want)
	}

	// No-frontmatter path.
	plain, err := commands.ParseStrict("Do "+testTag+"this\n", "review.md")
	if err != nil {
		t.Fatalf("ParseStrict(no frontmatter): %v", err)
	}
	wantWarning(t, plain.Warnings)

	// Lenient Parse path: malformed frontmatter is skipped, warning still set.
	lenient := commands.Parse("---\n: bad\n---\nDo "+testTag+"this\n", "review.md")
	wantWarning(t, lenient.Warnings)

	clean, err := commands.ParseStrict("---\ndescription: d\n---\nDo this\n", "review.md")
	if err != nil {
		t.Fatalf("ParseStrict(clean): %v", err)
	}
	if len(clean.Warnings) != 0 {
		t.Fatalf("clean command warnings = %q", clean.Warnings)
	}
}

func TestAutomationUnmarshalWarnsOnInvisibleContent(t *testing.T) {
	doc := `{"id":"t","name":"Task","schedule":{"kind":"interval","expr":"1h"},"prompt":"review ` + testTag + `this","enabled":true}`
	var spec automation.Spec
	if err := json.Unmarshal([]byte(doc), &spec); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	wantWarning(t, spec.Warnings)
	if want := "review " + testTag + "this"; spec.Prompt != want {
		t.Fatalf("prompt = %q, want unchanged %q", spec.Prompt, want)
	}
	assertNoWarningsField(t, spec)

	clean := `{"id":"t","name":"Task","schedule":{"kind":"interval","expr":"1h"},"prompt":"review this","enabled":true}`
	var cleanSpec automation.Spec
	if err := json.Unmarshal([]byte(clean), &cleanSpec); err != nil {
		t.Fatalf("Unmarshal(clean): %v", err)
	}
	if len(cleanSpec.Warnings) != 0 {
		t.Fatalf("clean task warnings = %q", cleanSpec.Warnings)
	}
}

// assertNoWarningsField guards the "no JSON shape change" contract.
func assertNoWarningsField(t *testing.T, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), "warning") {
		t.Fatalf("warnings leaked into JSON: %s", data)
	}
}
