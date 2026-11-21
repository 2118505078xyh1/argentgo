package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/commands"
)

// projectRootFromBuiltinTest 从当前包目录向上查找 go.mod，定位仓库根目录。
// go test 会把工作目录设为被测包目录，因此这里需要向上回溯。
func projectRootFromBuiltinTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test working directory")
		}
		dir = parent
	}
}

// TestBuiltinSkillsAreDiscoverable 校验随项目分发的三个内置 skill 能被现有
// Discover 机制解析，frontmatter 各字段都落在预期位置。
func TestBuiltinSkillsAreDiscoverable(t *testing.T) {
	root := projectRootFromBuiltinTest(t)
	skillsDir := filepath.Join(root, filepath.FromSlash(DefaultProjectSubdir))
	reg := Discover([]string{skillsDir})

	want := []struct {
		name        string
		desc        string
		when        string
		keywordHint string
	}{
		{"code-review", "多维度代码审查", "审查代码", "代码审查"},
		{"debug-workflow", "结构化排错流程", "bug", "复现"},
		{"test-writer", "补测试的方法论", "覆盖率", "表格驱动"},
	}
	for _, tc := range want {
		skill, err := reg.Load(tc.name)
		if err != nil {
			t.Errorf("Load(%q): %v (discovered: %v)", tc.name, err, skillNamesForBuiltinTest(reg))
			continue
		}
		if !strings.Contains(skill.Description, tc.desc) {
			t.Errorf("%s description = %q, want containing %q", tc.name, skill.Description, tc.desc)
		}
		if !strings.Contains(skill.WhenToUse, tc.when) {
			t.Errorf("%s when_to_use = %q, want containing %q", tc.name, skill.WhenToUse, tc.when)
		}
		// keywords 目前只是元数据（未接线到自动激活），但仍必须能被 frontmatter 解析保存。
		foundKeyword := false
		for _, kw := range skill.Triggers.Keywords {
			if strings.Contains(tc.keywordHint, kw) {
				foundKeyword = true
				break
			}
		}
		if !foundKeyword {
			t.Errorf("%s keywords = %v, want one matching %q", tc.name, skill.Triggers.Keywords, tc.keywordHint)
		}
		if strings.TrimSpace(skill.Body) == "" {
			t.Errorf("%s body is empty", tc.name)
		}
		if len(skill.Warnings) != 0 {
			t.Errorf("%s warnings = %v", tc.name, skill.Warnings)
		}
	}

	// 索引中每个技能只占一行，确保元数据不会向系统提示注入额外条目。
	// 第一行是固定说明，其余每行才是一个技能条目。
	lines := strings.Split(strings.TrimSpace(reg.IndexText()), "\n")
	if len(lines) != len(want)+1 {
		t.Errorf("index lines = %d, want %d", len(lines), len(want)+1)
	}
	for _, line := range lines[1:] {
		if !strings.HasPrefix(line, "- ") {
			t.Errorf("unexpected index line: %q", line)
		}
	}
}

// TestBuiltinCommandsAreDiscoverable 校验两个内置 slash 命令能被现有命令
// 发现机制解析，frontmatter 与 $ARGUMENTS 展开都按预期工作。
func TestBuiltinCommandsAreDiscoverable(t *testing.T) {
	root := projectRootFromBuiltinTest(t)
	set := commands.Discover(commands.DefaultSources(root, ""))

	want := []struct {
		name         string
		desc         string
		allowedTools []string
	}{
		{"commit", "conventional commit", []string{"bash"}},
		{"pr", "PR 描述", []string{"bash"}},
	}
	for _, tc := range want {
		cmd, ok := set.Get(tc.name)
		if !ok {
			t.Errorf("command %q not discovered (count=%d)", tc.name, set.Len())
			continue
		}
		if !strings.Contains(cmd.Description, tc.desc) {
			t.Errorf("%s description = %q, want containing %q", tc.name, cmd.Description, tc.desc)
		}
		for _, tool := range tc.allowedTools {
			found := false
			for _, got := range cmd.AllowedTools {
				if got == tool {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s allowed-tools = %v, want %q", tc.name, cmd.AllowedTools, tool)
			}
		}
		if strings.TrimSpace(cmd.ArgumentHint) == "" {
			t.Errorf("%s argument-hint is empty", tc.name)
		}
		if !strings.Contains(cmd.Body, "$ARGUMENTS") {
			t.Errorf("%s body does not contain $ARGUMENTS", tc.name)
		}
		if len(cmd.Warnings) != 0 {
			t.Errorf("%s warnings = %v", tc.name, cmd.Warnings)
		}
	}

	commit, ok := set.Get("commit")
	if !ok {
		return
	}
	expanded, err := commit.Expand("fix parser timeout")
	if err != nil {
		t.Fatalf("commit.Expand: %v", err)
	}
	if strings.Contains(expanded, "$ARGUMENTS") || !strings.Contains(expanded, "fix parser timeout") {
		t.Errorf("commit expansion did not replace $ARGUMENTS: %q", expanded)
	}
}

func skillNamesForBuiltinTest(reg *Registry) []string {
	all := reg.All()
	names := make([]string, 0, len(all))
	for _, skill := range all {
		names = append(names, skill.Name)
	}
	return names
}
