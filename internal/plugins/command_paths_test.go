package plugins

import (
	"path/filepath"
	"strings"
	"testing"
)

const commandDoc = "---\ndescription: open\n---\nOpen $ARGUMENTS\n"

// TestValidateAcceptsCommandDirectoryAndFile 固定 commands 贡献的双语义：
// 目录和单个命令文件都是合法声明，不应产生类型诊断。
func TestValidateAcceptsCommandDirectoryAndFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry string
		dir   bool
	}{
		{name: "directory", entry: "commands", dir: true},
		{name: "single file", entry: "commands/open.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.dir {
				writeFile(t, filepath.Join(dir, "commands", "open.md"), commandDoc)
			} else {
				writeFile(t, filepath.Join(dir, filepath.FromSlash(tc.entry)), commandDoc)
			}
			writeFile(t, filepath.Join(dir, "plugin.json"),
				`{"name":"alpha","version":"1.0.0","commands":["`+tc.entry+`"]}`)

			manifest, err := LoadManifest(dir)
			if err != nil {
				t.Fatalf("LoadManifest: %v", err)
			}
			if problems := manifest.Validate(); len(problems) != 0 {
				t.Fatalf("valid %s command declaration produced problems: %v", tc.name, problems)
			}
		})
	}
}

// TestValidateKeepsStrictTypesForSkillsAndHooks 确认放宽 commands 不影响
// 技能必须是目录、钩子文档必须是文件的校验。
func TestValidateKeepsStrictTypesForSkillsAndHooks(t *testing.T) {
	t.Run("skills require a directory", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "SKILL.md"), "# skill\n")
		writeFile(t, filepath.Join(dir, "plugin.json"),
			`{"name":"alpha","version":"1.0.0","skills":["SKILL.md"]}`)
		manifest, err := LoadManifest(dir)
		if err != nil {
			t.Fatalf("LoadManifest: %v", err)
		}
		joined := joinProblems(manifest.Validate())
		if !strings.Contains(joined, "skills path") || !strings.Contains(joined, "not a directory") {
			t.Fatalf("file declared as skills was not rejected: %s", joined)
		}
	})

	t.Run("hooks require a file", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "hooks", "keep.md"), "keep\n")
		writeFile(t, filepath.Join(dir, "plugin.json"),
			`{"name":"alpha","version":"1.0.0","hooks":{"paths":["hooks"]}}`)
		manifest, err := LoadManifest(dir)
		if err != nil {
			t.Fatalf("LoadManifest: %v", err)
		}
		joined := joinProblems(manifest.Validate())
		if !strings.Contains(joined, "hooks path") || !strings.Contains(joined, "not a file") {
			t.Fatalf("directory declared as hooks was not rejected: %s", joined)
		}
	})
}

// TestMergeResolvesCommandDirectoryAndFile 确认 Merge 保留两种声明的解析路径，
// 供接线层进一步归一：目录原样，单文件指向该 .md 文件。
func TestMergeResolvesCommandDirectoryAndFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry string
	}{
		{name: "directory", entry: "commands"},
		{name: "single file", entry: "commands/open.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "commands", "open.md"), commandDoc)
			writeFile(t, filepath.Join(dir, "plugin.json"),
				`{"name":"alpha","commands":["`+tc.entry+`"]}`)

			manifest, err := LoadManifest(dir)
			if err != nil {
				t.Fatalf("LoadManifest: %v", err)
			}
			wiring := Merge([]Bundle{{Name: manifest.Name, Dir: manifest.Root, Manifest: manifest, Enabled: true}})
			want := filepath.Join(dir, filepath.FromSlash(tc.entry))
			if len(wiring.Commands) != 1 || wiring.Commands[0] != want {
				t.Fatalf("commands = %v, want [%s]", wiring.Commands, want)
			}
		})
	}
}

func joinProblems(problems []Problem) string {
	var out strings.Builder
	for _, problem := range problems {
		out.WriteString(problem.String())
		out.WriteByte('\n')
	}
	return out.String()
}
