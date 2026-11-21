package eval

import (
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// benchFixturePath 返回仓库 evals/completion_bench.json 的路径。
// 用 runtime.Caller 而非相对 cwd，使测试在任意工作目录下都能定位到 fixture。
func benchFixturePath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "evals", "completion_bench.json")
}

// benchScriptTools 收集一个场景脚本中所有轮次请求的工具名。
func benchScriptTools(s Scenario) []string {
	var names []string
	for _, turn := range s.Provider {
		for _, call := range turn.Respond.ToolCalls {
			names = append(names, strings.TrimSpace(call.Name))
		}
	}
	return names
}

// benchNames 返回场景名序列，用于锁定 B1-B6 的顺序。
func benchNames(scenarios []Scenario) []string {
	names := make([]string, 0, len(scenarios))
	for _, s := range scenarios {
		names = append(names, s.Name)
	}
	return names
}

// TestCompletionBenchFixtureLoads 证明 R30 的 completion_bench.json 能被 eval.Load 解析，
// 且 B1-B6 的名字与顺序、脚本模式字段、客观 Check 都符合设计。
//
// 全部六个场景都是 Go 场景：allow_exec + go test ./...(want 0) + file_equals，
// 且每个场景的 file_equals 都逐字节锁死一份 *_test.go 种子内容，防止改测试作弊。
// B1/B3 额外声明 files_exist 覆盖 FilesExist 判据。
func TestCompletionBenchFixtureLoads(t *testing.T) {
	scenarios, err := Load(benchFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion_bench.json): %v", err)
	}
	if len(scenarios) != 6 {
		t.Fatalf("scenario count = %d, want 6", len(scenarios))
	}

	wantNames := []string{
		"B1 hashline anchored edit (should pass)",
		"B2 rename symbol across two files (should pass)",
		"B3 outline large file then precise edit (should pass)",
		"B4 ambiguous edit rejected then recovered (should pass)",
		"B5 negative control partial rename (should fail)",
		"B6 stale hashline anchor re-read then retry (should pass)",
	}
	if got := benchNames(scenarios); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("scenario names = %v, want %v", got, wantNames)
	}

	filesExistIndices := map[int]bool{0: true, 2: true}
	for i, s := range scenarios {
		t.Run(s.Name, func(t *testing.T) {
			// 脚本模式字段齐全：Provider、脚本工具、Expect.ToolCalls/TextContains/Turns 都必须非空。
			if len(s.Provider) == 0 {
				t.Error("provider must not be empty in script mode")
			}
			if tools := benchScriptTools(s); len(tools) == 0 {
				t.Error("script must request at least one tool call")
			}
			if len(s.Expect.ToolCalls) == 0 {
				t.Error("expect.tool_calls must be set in script mode")
			}
			if len(s.Expect.TextContains) == 0 {
				t.Error("expect.text_contains must be set in script mode")
			}
			if s.Expect.Turns == nil {
				t.Error("expect.turns must be set in script mode")
			}

			// 客观 Check：FileEquals 非空 + Go 场景判据 + 锁死 *_test.go 种子。
			if len(s.Check.FileEquals) == 0 {
				t.Fatal("check.file_equals must be present (objective grading)")
			}
			assertBenchGoCheck(t, s)

			if filesExistIndices[i] {
				if len(s.Check.FilesExist) == 0 {
					t.Error("this scenario must declare files_exist (FilesExist check)")
				}
			}
		})
	}
}

// assertBenchGoCheck 校验一个 Go 场景的客观判据：
// allow_exec + go test ./...(want 0) + file_equals，且 file_equals 锁死一份 *_test.go 种子内容。
func assertBenchGoCheck(t *testing.T, s Scenario) {
	t.Helper()
	if !s.Check.AllowExec || s.Check.Exec == nil {
		t.Fatal("check must enable exec")
	}
	exec := s.Check.Exec
	if exec.Command != "go" {
		t.Errorf("exec command = %q, want go", exec.Command)
	}
	if want := []string{"test", "./..."}; !reflect.DeepEqual(exec.Args, want) {
		t.Errorf("exec args = %v, want %v", exec.Args, want)
	}
	if exec.WantExitCode != 0 {
		t.Errorf("exec want_exit_code = %d, want 0", exec.WantExitCode)
	}

	locked := false
	for path, want := range s.Check.FileEquals {
		if !strings.HasSuffix(path, "_test.go") {
			continue
		}
		locked = true
		if got, ok := s.Files[path]; !ok || got != want {
			t.Errorf("file_equals locks %q to %q, but seed is %q (present=%t)", path, want, got, ok)
		}
	}
	if !locked {
		t.Error("check.file_equals must lock down a *_test.go seed to prevent test tampering")
	}
}

// TestCompletionBenchNamesDoNotCollide 证明 B1-B6 的名字与已有的 completion.json / completion_expanded.json 场景不重复。
func TestCompletionBenchNamesDoNotCollide(t *testing.T) {
	bench, err := Load(benchFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion_bench.json): %v", err)
	}
	seen := make(map[string]bool)
	for _, s := range bench {
		if seen[s.Name] {
			t.Errorf("duplicate scenario name %q", s.Name)
		}
		seen[s.Name] = true
	}

	// 已有场景集的名字必须全部不在新集合中。
	for _, path := range []string{completionFixturePath(t), expandedFixturePath(t)} {
		existing, err := Load(path)
		if err != nil {
			t.Fatalf("Load(%s): %v", path, err)
		}
		for _, s := range existing {
			if seen[s.Name] {
				t.Errorf("scenario name %q collides with an existing completion scenario", s.Name)
			}
		}
	}
}

// TestCompletionBenchNegativeSharesSeedAndCheck 锁定 B5 负对照与 B2 的契约：
// 同种子（files）、同 Check，只有脚本/期望不同，因此二者结果差异只能来自是否真的跨文件改对。
func TestCompletionBenchNegativeSharesSeedAndCheck(t *testing.T) {
	scenarios, err := Load(benchFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion_bench.json): %v", err)
	}
	b2, b5 := scenarios[1], scenarios[4]
	if !reflect.DeepEqual(b2.Files, b5.Files) {
		t.Error("B5 must share B2's seed files")
	}
	if !reflect.DeepEqual(b2.Check, b5.Check) {
		t.Error("B5 must share B2's Check byte-for-byte")
	}
	if reflect.DeepEqual(b2.Provider, b5.Provider) {
		t.Error("B5 script must differ from B2 (it renames only the definition)")
	}
}
