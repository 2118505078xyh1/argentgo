package plugins

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestMergeDeduplicatesResolvedPaths checks that a manifest naming the same
// source more than once contributes a single wiring entry. Resolution must
// happen before comparison so that "skills", "./skills" and "sub/../skills"
// collapse to one path.
func TestMergeDeduplicatesResolvedPaths(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name     string
		category string // "skills", "commands" or "hooks"
		paths    []string
		want     []string
	}{
		{
			name: "distinct paths are all kept", category: "skills",
			paths: []string{"skills", "other"},
			want:  []string{"skills", "other"},
		},
		{
			name: "exact duplicates collapse", category: "skills",
			paths: []string{"skills", "skills"},
			want:  []string{"skills"},
		},
		{
			name: "different spellings of one path collapse", category: "skills",
			paths: []string{"skills", "./skills", "sub/../skills"},
			want:  []string{"skills"},
		},
		{
			name: "duplicate commands collapse", category: "commands",
			paths: []string{"commands/open.md", "commands/./open.md"},
			want:  []string{"commands/open.md"},
		},
		{
			name: "duplicate hook documents collapse", category: "hooks",
			paths: []string{"hooks.json", "hooks.json"},
			want:  []string{"hooks.json"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manifest := Manifest{
				Name: "p",
				Root: root,
				Path: filepath.Join(root, "plugin.json"),
			}
			switch tc.category {
			case "skills":
				manifest.Skills = tc.paths
			case "commands":
				manifest.Commands = tc.paths
			case "hooks":
				manifest.Hooks.Paths = tc.paths
			}

			wiring := Merge([]Bundle{{
				Name: "p", Dir: root, Manifest: manifest, Enabled: true,
			}})

			var got []string
			switch tc.category {
			case "skills":
				got = wiring.Skills
			case "commands":
				got = wiring.Commands
			case "hooks":
				got = wiring.HooksPaths
			}
			want := make([]string, 0, len(tc.want))
			for _, relative := range tc.want {
				want = append(want, filepath.Join(root, filepath.FromSlash(relative)))
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("wiring %s = %q, want %q", tc.category, got, want)
			}
			if len(wiring.problems) != 0 {
				t.Fatalf("unexpected problems: %v", wiring.problems)
			}
		})
	}
}

// TestAppendResolvedDedupDoesNotHideEscapes makes sure deduplication is applied
// only after the bundle-root check: a repeated escaping path is still refused
// and reported, never emitted once as a "unique" entry.
func TestAppendResolvedDedupDoesNotHideEscapes(t *testing.T) {
	root := t.TempDir()
	manifest, err := ParseManifest([]byte(`{"name":"p","skills":["../../outside","../../outside","skills"]}`), root, "")
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	var wiring Wiring
	got := wiring.appendResolved(nil, Bundle{Name: "p", Manifest: manifest}, manifest.Skills)
	want := []string{filepath.Join(root, "skills")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved = %q, want %q", got, want)
	}
	if len(wiring.problems) != 2 {
		t.Fatalf("problems = %v, want two escape findings", wiring.problems)
	}
}
