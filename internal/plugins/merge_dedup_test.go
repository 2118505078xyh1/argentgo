package plugins

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestMergeDeduplicatesResolvedPaths 检查多次命名同一
// 来源的清单只贡献一个接线条目。解析必须在
// TestMergeDeduplicatesResolvedPaths 检查多次命名同一来源的清单只贡献一个接线条目。解析必须在比较之前发生，使 "skills"、"./skills" 和 "sub/../skills" 折叠为一个路径。
// 折叠为一个路径。
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

// TestAppendResolvedDedupDoesNotHideEscapes 确保去重在
// TestAppendResolvedDedupDoesNotHideEscapes 确保去重只在 bundle 根检查之后应用：重复的逃逸路径仍被拒绝和报告，绝不以"唯一"条目发出一次。
// 且被报告，绝不以"唯一"条目发出一次。
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
