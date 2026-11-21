package eval

import (
	"path/filepath"
	"runtime"
	"testing"
)

// completionFixturePath 返回仓库 evals/completion.json 的路径。
// 用 runtime.Caller 而非相对 cwd，使测试在任意工作目录下都能定位到 fixture。
func completionFixturePath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "evals", "completion.json")
}

// completionScriptTools 收集一个场景脚本中所有轮次请求的工具名。
func completionScriptTools(s Scenario) []string {
	var names []string
	for _, turn := range s.Provider {
		for _, call := range turn.Respond.ToolCalls {
			names = append(names, call.Name)
		}
	}
	return names
}

// completionHasTool 报告 names 中是否包含指定工具名。
func completionHasTool(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// TestCompletionFixtureLoadsFixAndNoop 证明 R24 的完成率场景文档能被 eval.Load 解析，
// 且 F/N 两个场景的脚本与 Check 符合 R23 的语义：F 真修 bug（有 edit），N 只读不改（无 edit），
// 两者共用同一份 go test Exec 与 calc.go 的 FileEquals 逐字节比对。
func TestCompletionFixtureLoadsFixAndNoop(t *testing.T) {
	scenarios, err := Load(completionFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion.json): %v", err)
	}
	if len(scenarios) != 2 {
		t.Fatalf("scenario count = %d, want 2", len(scenarios))
	}

	fix, noop := scenarios[0], scenarios[1]
	if want := "fix the subtraction bug (should pass)"; fix.Name != want {
		t.Errorf("fix scenario name = %q, want %q", fix.Name, want)
	}
	if want := "read only, no fix (should fail)"; noop.Name != want {
		t.Errorf("noop scenario name = %q, want %q", noop.Name, want)
	}

	t.Run("fix scripts a read then an edit", func(t *testing.T) {
		tools := completionScriptTools(fix)
		if !completionHasTool(tools, "read") || !completionHasTool(tools, "edit") {
			t.Fatalf("fix script tools = %v, want read and edit", tools)
		}
		if len(fix.Provider) != 3 {
			t.Fatalf("fix provider turns = %d, want 3", len(fix.Provider))
		}
		wantTools := []string{"read", "edit"}
		if len(fix.Expect.ToolCalls) != len(wantTools) ||
			fix.Expect.ToolCalls[0] != wantTools[0] || fix.Expect.ToolCalls[1] != wantTools[1] {
			t.Errorf("fix expect tools = %v, want %v", fix.Expect.ToolCalls, wantTools)
		}
	})

	t.Run("noop scripts only a read", func(t *testing.T) {
		tools := completionScriptTools(noop)
		if completionHasTool(tools, "edit") {
			t.Fatalf("noop script must not edit; tools = %v", tools)
		}
		if len(tools) != 1 || tools[0] != "read" {
			t.Fatalf("noop script tools = %v, want a single read", tools)
		}
		if len(noop.Expect.ToolCalls) != 1 || noop.Expect.ToolCalls[0] != "read" {
			t.Errorf("noop expect tools = %v, want [read]", noop.Expect.ToolCalls)
		}
	})

	t.Run("both scenarios share the exec and file equals check", func(t *testing.T) {
		const fixed = "package calc\n\nfunc Add(a, b int) int { return a + b }\n"
		for _, s := range []Scenario{fix, noop} {
			if !s.Check.AllowExec || s.Check.Exec == nil {
				t.Errorf("%s: check must enable exec", s.Name)
				continue
			}
			if s.Check.Exec.Command != "go" {
				t.Errorf("%s: exec command = %q, want go", s.Name, s.Check.Exec.Command)
			}
			if len(s.Check.Exec.Args) != 2 || s.Check.Exec.Args[0] != "test" || s.Check.Exec.Args[1] != "./..." {
				t.Errorf("%s: exec args = %v, want [test ./...]", s.Name, s.Check.Exec.Args)
			}
			if s.Check.Exec.WantExitCode != 0 {
				t.Errorf("%s: exec want_exit_code = %d, want 0", s.Name, s.Check.Exec.WantExitCode)
			}
			if got := s.Check.FileEquals["calc.go"]; got != fixed {
				t.Errorf("%s: calc.go FileEquals = %q, want %q", s.Name, got, fixed)
			}
		}
	})

	t.Run("expectations use script-mode fields", func(t *testing.T) {
		for _, s := range []Scenario{fix, noop} {
			if s.Expect.Turns == nil {
				t.Errorf("%s: expect.turns must be set in script mode", s.Name)
			}
			if len(s.Expect.TextContains) == 0 {
				t.Errorf("%s: expect.text_contains must be set in script mode", s.Name)
			}
		}
	})
}
