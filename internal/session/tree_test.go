package session

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func forkedTranscript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forked.jsonl")
	stamp := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	writeRawSession(t, path, []Record{
		{ID: "n1", TS: stamp, Kind: KindHeader, Data: json.RawMessage(`{"title":"fork demo"}`)},
		{ID: "n2", Parent: "n1", TS: stamp, Kind: KindUser, Data: json.RawMessage(`"root question"`)},
		{ID: "n3", Parent: "n2", TS: stamp, Kind: KindAssistant, Data: json.RawMessage(`"branch A"`)},
		{ID: "n4", Parent: "n2", TS: stamp, Kind: KindAssistant, Data: json.RawMessage(`"branch B <script>alert(1)</script>"`)},
	})
	return path
}

func TestBranchesDescribeForkedTree(t *testing.T) {
	branches, err := Branches(forkedTranscript(t))
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if len(branches) != 4 {
		t.Fatalf("branches = %d, want 4", len(branches))
	}
	wantIDs := []string{"n1", "n2", "n3", "n4"}
	wantParents := []string{"", "n1", "n2", "n2"}
	wantDepths := []int{0, 1, 2, 2}
	for i, want := range wantIDs {
		if branches[i].ID != want || branches[i].Parent != wantParents[i] || branches[i].Depth != wantDepths[i] {
			t.Errorf("branch %d = %+v, want id=%s parent=%s depth=%d", i, branches[i], want, wantParents[i], wantDepths[i])
		}
	}
	if !branches[3].Active || branches[2].Active {
		t.Fatalf("active flags = %v/%v, want only n4 active", branches[2].Active, branches[3].Active)
	}
}

// TestBranchDepthCachesUnderOriginalID covers session audit #6: the depth of
// a chain must be cached under the id whose depth was requested, not under the
// top ancestor the walk ended on. Otherwise a later descendant of that
// ancestor inherits the whole chain's depth.
func TestBranchDepthCachesUnderOriginalID(t *testing.T) {
	byID := map[string]Record{
		"A":    {ID: "A", Parent: "B"},
		"B":    {ID: "B", Parent: "ROOT"},
		"ROOT": {ID: "ROOT"},
	}
	cache := make(map[string]int)
	if got := branchDepth("A", byID, cache); got != 2 {
		t.Fatalf("depth(A) = %d, want 2", got)
	}
	if got := cache["A"]; got != 2 {
		t.Fatalf("cache[A] = %d, want 2", got)
	}
	if _, ok := cache["ROOT"]; ok {
		t.Fatalf("ancestor ROOT was cached with accumulated depth %d", cache["ROOT"])
	}
	if got := branchDepth("B", byID, cache); got != 1 {
		t.Fatalf("depth(B) = %d, want 1 (cached ancestor polluted it)", got)
	}
	if got := branchDepth("ROOT", byID, cache); got != 0 {
		t.Fatalf("depth(ROOT) = %d, want 0", got)
	}
}

// TestBranchesDescribeOutOfOrderTree exercises audit #6 through the public
// path: children are listed before their parents, as happens with a repaired
// or hand-assembled transcript.
func TestBranchesDescribeOutOfOrderTree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out-of-order.jsonl")
	writeRawSession(t, path, []Record{
		{ID: "A", Parent: "B", Kind: KindUser, Data: json.RawMessage(`"a"`)},
		{ID: "B", Parent: "ROOT", Kind: KindUser, Data: json.RawMessage(`"b"`)},
		{ID: "ROOT", Kind: KindHeader, Data: json.RawMessage(`{"title":"root"}`)},
	})
	branches, err := Branches(path)
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	want := map[string]int{"A": 2, "B": 1, "ROOT": 0}
	if len(branches) != len(want) {
		t.Fatalf("branches = %d, want %d", len(branches), len(want))
	}
	for _, branch := range branches {
		if depth, ok := want[branch.ID]; !ok || branch.Depth != depth {
			t.Errorf("branch %q depth = %d, want %d", branch.ID, branch.Depth, depth)
		}
	}
}

func TestRenderHTMLEscapesTextAndIsSelfContained(t *testing.T) {
	var out bytes.Buffer
	if err := RenderHTML(forkedTranscript(t), "", &out); err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	html := out.String()
	for _, want := range []string{"<!doctype html>", "fork demo", "branch B", "role-assistant", "<style>"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	if strings.Contains(html, "branch A") {
		t.Fatalf("default render followed the inactive branch: %s", html)
	}
	if strings.Contains(html, "<script") || strings.Contains(html, "alert(1)</script>") {
		t.Fatalf("unescaped script in HTML: %s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Fatalf("script text not escaped: %s", html)
	}
	for _, forbidden := range []string{"http://", "https://", "cdn.", "src=", "<link", "fetch("} {
		if strings.Contains(strings.ToLower(html), forbidden) {
			t.Errorf("HTML references network resource %q", forbidden)
		}
	}
}

func TestRenderHTMLSelectsRequestedBranch(t *testing.T) {
	var out bytes.Buffer
	if err := RenderHTML(forkedTranscript(t), "n3", &out); err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	html := out.String()
	if !strings.Contains(html, "branch A") || strings.Contains(html, "branch B") {
		t.Fatalf("branch selection = %s", html)
	}
}

func TestBranchesAndRenderHTMLRejectMissingSession(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.jsonl")
	if _, err := Branches(missing); err == nil {
		t.Fatal("Branches(missing) succeeded")
	}
	var out bytes.Buffer
	if err := RenderHTML(missing, "", &out); err == nil {
		t.Fatal("RenderHTML(missing) succeeded")
	}
}
