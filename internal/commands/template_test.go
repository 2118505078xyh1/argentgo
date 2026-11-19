package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpansionGroupsQuotedAndUnicodeArguments(t *testing.T) {
	cmd := Command{Name: "x", Body: "$1|$2|$3|$ARGUMENTS"}
	got, err := cmd.Expand(`"two words" '中文 标签' plain`)
	if err != nil {
		t.Fatal(err)
	}
	want := `two words|中文 标签|plain|"two words" '中文 标签' plain`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRemainingAtHighestIsOptIn(t *testing.T) {
	cmd := Command{Name: "x", Body: "$1|$2"}
	plain, err := cmd.Expand("a b c d")
	if err != nil || plain != "a|b" {
		t.Fatalf("default = %q, %v", plain, err)
	}
	got, err := cmd.ExpandWith("a b c d", ExpandOptions{RemainingAtHighest: true})
	if err != nil || got != "a|b c d" {
		t.Fatalf("remaining = %q, %v", got, err)
	}
}

func TestDoubleDigitAndBracedPositions(t *testing.T) {
	args := "a b c d e f g h i j k l"
	cmd := Command{Name: "x", Body: "$10 ${12} $1"}
	got, err := cmd.Expand(args)
	if err != nil || got != "j l a" {
		t.Fatalf("got %q, %v", got, err)
	}
	missing := Command{Name: "x", Body: "${20}"}
	if got, err := missing.Expand(args); err != nil || got != "" {
		t.Fatalf("missing position = %q, %v", got, err)
	}
}

func TestParseStrictReadsAgentAndModelFrontmatter(t *testing.T) {
	text := "---\ndescription: review\nmodel: gpt-5\nagent: reviewer\n---\nReview $ARGUMENTS"
	cmd, err := ParseStrict(text, filepath.Join("cmds", "review.md"))
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Model != "gpt-5" || cmd.Agent != "reviewer" {
		t.Fatalf("frontmatter = %+v", cmd)
	}
}

func TestParseStrictReportsInvalidFrontmatterWithFileAndLine(t *testing.T) {
	text := "---\ndescription: valid\nthis is not a field\n---\nbody"
	_, err := ParseStrict(text, "broken.md")
	if err == nil || !strings.Contains(err.Error(), "broken.md:3") {
		t.Fatalf("error = %v, want broken.md:3", err)
	}
}

func TestDiscoverySkipsMalformedCommandsAndKeepsPrecedence(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	projectRoot, homeRoot := filepath.Join(project, filepath.FromSlash(ProjectSubdir)), filepath.Join(home, filepath.FromSlash(HomeSubdir))
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(homeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "review.md"), []byte("---\ndescription: project\n---\nproject body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeRoot, "review.md"), []byte("---\ndescription: user\nbroken line\n---\nuser body"), 0o644); err != nil {
		t.Fatal(err)
	}
	set := Discover(DefaultSources(project, home))
	cmd, ok := set.Get("review")
	if !ok || !strings.Contains(cmd.Body, "project") {
		t.Fatalf("discovered = %+v", set.List())
	}
}

func TestExpansionNeverRunsShellSpansByDefault(t *testing.T) {
	cmd := Command{Name: "x", Body: "!`touch /tmp/should-not-exist` $1"}
	got, err := cmd.ExpandWith("arg", ExpandOptions{RemainingAtHighest: true})
	if err != nil || !strings.Contains(got, "!`touch") || !strings.HasSuffix(got, "arg") {
		t.Fatalf("expansion = %q, %v", got, err)
	}
}
