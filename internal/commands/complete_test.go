package commands

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestCompleteCommandsPrefixMatching(t *testing.T) {
	cmds := []Command{
		{Name: "review", Description: "review diff"},
		{Name: "revive", Description: "revive session"},
		{Name: "context", Description: "show context"},
		{Name: "deploy", Description: "deploy site"},
	}
	tests := []struct {
		name   string
		prefix string
		want   []string
	}{
		{name: "plain prefix", prefix: "rev", want: []string{"review", "revive"}},
		{name: "case insensitive", prefix: "RE", want: []string{"review", "revive"}},
		{name: "full name", prefix: "deployment", want: nil},
		{name: "exact name", prefix: "context", want: []string{"context"}},
		{name: "unmatched", prefix: "z", want: nil},
		{name: "leading slash", prefix: "/rev", want: []string{"review", "revive"}},
		{name: "trailing argument ignored", prefix: "review --all", want: []string{"review"}},
		{name: "empty prefix lists all sorted", prefix: "", want: []string{"context", "deploy", "review", "revive"}},
		{name: "whitespace prefix lists all", prefix: "  ", want: []string{"context", "deploy", "review", "revive"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := namesOf(CompleteCommands(cmds, tt.prefix))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CompleteCommands(%q) = %v, want %v", tt.prefix, got, tt.want)
			}
		})
	}
}

func TestCompleteCommandsCJKAndFullWidth(t *testing.T) {
	cmds := []Command{
		{Name: "审查", Description: "CJK command"},
		{Name: "审核日志", Description: "CJK command"},
		{Name: "review", Description: "ascii command"},
	}
	tests := []struct {
		name   string
		prefix string
		want   []string
	}{
		{name: "cjk rune prefix", prefix: "审", want: []string{"审查", "审核日志"}},
		{name: "full cjk name", prefix: "审查", want: []string{"审查"}},
		{name: "full width ascii folds", prefix: "ＲＥＶ", want: []string{"review"}},
		{name: "full width slash folds", prefix: "／ｒｅｖ", want: []string{"review"}},
		{name: "ideographic space ends first token", prefix: "审查　额外", want: []string{"审查"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := namesOf(CompleteCommands(cmds, tt.prefix))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CompleteCommands(%q) = %v, want %v", tt.prefix, got, tt.want)
			}
		})
	}
}

func TestParseStrictAliases(t *testing.T) {
	text := "---\nname: review\naliases: rv, 复审, code-review\n---\nReview the diff."
	cmd, err := ParseStrict(text, filepath.Join("tmp", "review.md"))
	if err != nil {
		t.Fatalf("ParseStrict: %v", err)
	}
	want := []string{"rv", "复审", "code-review"}
	if !reflect.DeepEqual(cmd.Aliases, want) {
		t.Errorf("aliases = %#v, want %#v", cmd.Aliases, want)
	}
}

func TestCompleteCommandsAliases(t *testing.T) {
	cmds := []Command{
		{Name: "review", Aliases: []string{"rv", "复审"}, Description: "review diff"},
		{Name: "context", Aliases: []string{"ctx"}, Description: "show context"},
		{Name: "acme:review", Plugin: "acme", Aliases: []string{"rv"}, Description: "plugin review"},
	}
	tests := []struct {
		name   string
		prefix string
		want   []string
	}{
		{name: "short alias", prefix: "rv", want: []string{"acme:review", "review"}},
		{name: "cjk alias", prefix: "复", want: []string{"review"}},
		{name: "namespace alias", prefix: "acme:rv", want: []string{"acme:review"}},
		{name: "namespace still matches name", prefix: "acme", want: []string{"acme:review"}},
		{name: "alias case insensitive", prefix: "CTX", want: []string{"context"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := namesOf(CompleteCommands(cmds, tt.prefix))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CompleteCommands(%q) = %v, want %v", tt.prefix, got, tt.want)
			}
		})
	}
}

func TestCompleteCommandsSortedAndUnique(t *testing.T) {
	// Deliberately unsorted input, with a command matching by both name and
	// alias, and two commands sharing a name from different paths.
	cmds := []Command{
		{Name: "zeta", Aliases: []string{"rev"}, Path: "z.md"},
		{Name: "review", Path: "b.md"},
		{Name: "review", Path: "a.md"},
		{Name: "revive", Path: "r.md"},
	}
	got := CompleteCommands(cmds, "rev")
	if len(got) != 4 {
		t.Fatalf("got %d results, want 4: %#v", len(got), got)
	}
	wantOrder := []struct{ name, path string }{
		{"review", "a.md"},
		{"review", "b.md"},
		{"revive", "r.md"},
		{"zeta", "z.md"},
	}
	for i, want := range wantOrder {
		if got[i].Name != want.name || got[i].Path != want.path {
			t.Errorf("result %d = %s/%s, want %s/%s", i, got[i].Name, got[i].Path, want.name, want.path)
		}
	}
	// The "review" command matched by alias must appear exactly once.
	count := 0
	for _, c := range got {
		if c.Name == "zeta" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("command matched by name and alias appeared %d times, want 1", count)
	}
}

func TestCompleteCommandsDoesNotMutateInput(t *testing.T) {
	cmds := []Command{
		{Name: "zeta", Path: "z.md"},
		{Name: "review", Path: "r.md"},
	}
	before := append([]Command(nil), cmds...)
	got := CompleteCommands(cmds, "")
	if got[0].Name != "review" {
		t.Fatalf("result should be sorted, got %q first", got[0].Name)
	}
	if !reflect.DeepEqual(cmds, before) {
		t.Errorf("input slice was modified: %#v", cmds)
	}
	got[0].Name = "tampered"
	if cmds[1].Name != "review" {
		t.Error("result aliases the input slice")
	}
}

func TestSetComplete(t *testing.T) {
	set := &Set{}
	set.Add(
		Command{Name: "deploy", Path: "d.md"},
		Command{Name: "review", Path: "r.md"},
	)
	got := namesOf(set.Complete("dep"))
	want := []string{"deploy"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Set.Complete = %v, want %v", got, want)
	}
	var nilSet *Set
	if result := nilSet.Complete("x"); len(result) != 0 {
		t.Errorf("nil Set.Complete = %v, want empty", result)
	}
}

func namesOf(cmds []Command) []string {
	if len(cmds) == 0 {
		return nil
	}
	out := make([]string, 0, len(cmds))
	for _, cmd := range cmds {
		out = append(out, cmd.Name)
	}
	return out
}
