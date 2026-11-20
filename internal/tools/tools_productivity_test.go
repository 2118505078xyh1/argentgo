package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- fake injected sources ----

type fakeUsageSource struct {
	json string
}

func (f *fakeUsageSource) UsageJSON() string { return f.json }

type fakeHistorySource struct {
	query  string
	limit  int
	result string
}

func (f *fakeHistorySource) Search(query string, limit int) string {
	f.query, f.limit = query, limit
	return f.result
}

func writeProductivityFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---- diff ----

func TestDiffComparesLineByLine(t *testing.T) {
	cases := []struct {
		name    string
		a       string
		b       string
		wantSub []string
		notWant []string
	}{
		{
			name:    "identical files",
			a:       "alpha\nbeta\ngamma\n",
			b:       "alpha\nbeta\ngamma\n",
			wantSub: []string{"+0 added  -0 removed  ~0 changed", "no differences"},
		},
		{
			name:    "one changed line",
			a:       "alpha\nbeta\ngamma\n",
			b:       "alpha\nBETA\ngamma\n",
			wantSub: []string{"+0 added  -0 removed  ~1 changed", "-beta", "+BETA"},
		},
		{
			name:    "added lines",
			a:       "alpha\ngamma\n",
			b:       "alpha\nbeta\ngamma\n",
			wantSub: []string{"+1 added  -0 removed  ~0 changed", "+beta"},
		},
		{
			name:    "removed lines",
			a:       "alpha\nbeta\ngamma\n",
			b:       "alpha\ngamma\n",
			wantSub: []string{"+0 added  -1 removed  ~0 changed", "-beta"},
		},
		{
			name:    "CJK text",
			a:       "你好\n世界\n",
			b:       "你好\n世界！\n",
			wantSub: []string{"~1 changed", "-世界", "+世界！"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeProductivityFile(t, dir, "a.txt", tc.a)
			writeProductivityFile(t, dir, "b.txt", tc.b)
			args, _ := json.Marshal(map[string]string{"path_a": "a.txt", "path_b": "b.txt"})
			out, err := diffTool{}.Run(context.Background(), dir, args)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.wantSub {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
			for _, bad := range tc.notWant {
				if strings.Contains(out, bad) {
					t.Errorf("output unexpectedly contains %q:\n%s", bad, out)
				}
			}
		})
	}
}

func TestDiffRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	writeProductivityFile(t, dir, "inside.txt", "x\n")
	args, _ := json.Marshal(map[string]string{"path_a": "../outside.txt", "path_b": "inside.txt"})
	_, err := diffTool{}.Run(context.Background(), dir, args)
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("path escape: err = %v, want workspace rejection", err)
	}
}

func TestDiffOutputBounded(t *testing.T) {
	const limit = 200
	dir := t.TempDir()
	var a, b strings.Builder
	for i := 0; i < 60; i++ {
		a.WriteString(strings.Repeat("a", 120) + "\n")
		b.WriteString(strings.Repeat("b", 120) + "\n")
	}
	writeProductivityFile(t, dir, "a.txt", a.String())
	writeProductivityFile(t, dir, "b.txt", b.String())

	registry := NewWithLimit(dir, true, limit)
	args, _ := json.Marshal(map[string]string{"path_a": "a.txt", "path_b": "b.txt"})
	out, err := registry.Run(context.Background(), "diff", string(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[truncated]") {
		t.Fatalf("large diff was not clipped:\n%s", out)
	}
	if len(out) > limit+256 {
		t.Fatalf("output length %d exceeds cap %d", len(out), limit)
	}
}

// ---- usage ----

func TestUsageSourceInjection(t *testing.T) {
	const aJSON = `{"prompt_tokens":10,"cost_usd":0.01}`
	const bJSON = `{"prompt_tokens":99,"cost_usd":0.42}`

	t.Run("without source reports unavailable", func(t *testing.T) {
		r := New(t.TempDir(), true)
		out, err := r.Run(context.Background(), "usage", `{}`)
		if err != nil {
			t.Fatal(err)
		}
		if out != usageUnavailable {
			t.Fatalf("usage = %q, want %q", out, usageUnavailable)
		}
	})

	t.Run("source returns its JSON and swapping replaces it", func(t *testing.T) {
		r := New(t.TempDir(), true)
		r.SetUsageSource(&fakeUsageSource{json: aJSON})
		out, err := r.Run(context.Background(), "usage", `{}`)
		if err != nil || out != aJSON {
			t.Fatalf("usage = %q, err = %v, want %q", out, err, aJSON)
		}
		r.SetUsageSource(&fakeUsageSource{json: bJSON})
		out, err = r.Run(context.Background(), "usage", `{}`)
		if err != nil || out != bJSON {
			t.Fatalf("usage after swap = %q, err = %v, want %q", out, err, bJSON)
		}
	})
}

// ---- history ----

func TestHistorySourceInjection(t *testing.T) {
	t.Run("without source reports unavailable", func(t *testing.T) {
		r := New(t.TempDir(), true)
		out, err := r.Run(context.Background(), "history", `{}`)
		if err != nil {
			t.Fatal(err)
		}
		if out != historyUnavailable {
			t.Fatalf("history = %q, want %q", out, historyUnavailable)
		}
	})

	t.Run("query and limit are passed through", func(t *testing.T) {
		r := New(t.TempDir(), true)
		fake := &fakeHistorySource{result: "match one\nmatch two"}
		r.SetHistorySource(fake)
		out, err := r.Run(context.Background(), "history", `{"query":"needle","limit":7}`)
		if err != nil {
			t.Fatal(err)
		}
		if out != fake.result {
			t.Fatalf("history = %q, want %q", out, fake.result)
		}
		if fake.query != "needle" || fake.limit != 7 {
			t.Fatalf("source got query=%q limit=%d; want needle/7", fake.query, fake.limit)
		}
	})

	t.Run("swapping source keeps registration order", func(t *testing.T) {
		r := New(t.TempDir(), true)
		before := strings.Join(r.Names(), ",")
		r.SetHistorySource(&fakeHistorySource{result: "a"})
		r.SetHistorySource(&fakeHistorySource{result: "b"})
		if got := strings.Join(r.Names(), ","); got != before {
			t.Fatalf("registration order changed:\nbefore %s\nafter  %s", before, got)
		}
		out, err := r.Run(context.Background(), "history", `{}`)
		if err != nil || out != "b" {
			t.Fatalf("history after swap = %q, err = %v, want b", out, err)
		}
	})
}

// ---- metadata / catalog ----

func TestProductivityToolsMetadata(t *testing.T) {
	cases := []struct {
		name     string
		mutating bool
		tool     Tool
	}{
		{"diff", false, diffTool{}},
		{"usage", false, &usageTool{}},
		{"history", false, &historyTool{}},
	}
	registry := New(t.TempDir(), true)
	names := registry.Names()
	for _, tc := range cases {
		if tc.tool.Name() != tc.name {
			t.Errorf("Name() = %q, want %q", tc.tool.Name(), tc.name)
		}
		if tc.tool.Mutating() != tc.mutating {
			t.Errorf("%s Mutating() = %v, want %v", tc.name, tc.tool.Mutating(), tc.mutating)
		}
		schema := tc.tool.Schema()
		if !json.Valid([]byte(schema)) {
			t.Errorf("%s schema is not valid JSON: %s", tc.name, schema)
		}
		if len(schema) >= 1024 {
			t.Errorf("%s schema is %d bytes, want < 1024", tc.name, len(schema))
		}
		found := false
		for _, n := range names {
			if n == tc.name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s not in Names(): %v", tc.name, names)
		}
	}
}
