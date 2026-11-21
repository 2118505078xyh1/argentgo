package eval

import (
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// expandedFixturePath 返回仓库 evals/completion_expanded.json 的路径。
// 用 runtime.Caller 而非相对 cwd，使测试在任意工作目录下都能定位到 fixture。
func expandedFixturePath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "evals", "completion_expanded.json")
}

// expandedNames 返回场景名序列，用于锁定 S1-S5 的顺序。
func expandedNames(scenarios []Scenario) []string {
	names := make([]string, 0, len(scenarios))
	for _, s := range scenarios {
		names = append(names, s.Name)
	}
	return names
}

// expandedScriptTools 收集一个场景脚本中所有轮次请求的工具名。
func expandedScriptTools(s Scenario) []string {
	var names []string
	for _, turn := range s.Provider {
		for _, call := range turn.Respond.ToolCalls {
			names = append(names, strings.TrimSpace(call.Name))
		}
	}
	return names
}

// TestCompletionExpandedFixtureLoads 证明 R25 扩充场景文档能被 eval.Load 解析，
// 且 S1-S5 的名字与顺序、脚本模式字段、客观 Check 都符合设计。
//
// 四个正例（S1/S2/S3/S4）加一个负例（S5）：Go 场景用 allow_exec + go test + file_equals，
// S3 用 files_exist + file_equals 且无 Exec；每个 Go 场景的 file_equals 都逐字节锁死
// 一份 *_test.go 种子内容，防止模型改测试作弊。
func TestCompletionExpandedFixtureLoads(t *testing.T) {
	scenarios, err := Load(expandedFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion_expanded.json): %v", err)
	}

	wantNames := []string{
		"S1 refactor across two files (should pass)",
		"S2 debug two bugs to green (should pass)",
		"S3 needle search and record (should pass)",
		"S4 orchestrate read grep edit write (should pass)",
		"S5 negative control fix one bug only (should fail)",
	}
	if got := expandedNames(scenarios); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("scenario names = %v, want %v", got, wantNames)
	}

	// 只有 S3 是无 Exec 的 needle 场景；其余四个 Go 场景必须走 go test 判据。
	goIndices := map[int]bool{0: true, 1: true, 3: true, 4: true}
	for i, s := range scenarios {
		t.Run(s.Name, func(t *testing.T) {
			// 脚本模式字段齐全：Provider、Expect.ToolCalls/TextContains/Turns 都必须非空。
			if len(s.Provider) == 0 {
				t.Error("provider must not be empty in script mode")
			}
			if tools := expandedScriptTools(s); len(tools) == 0 {
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
			if len(s.Check.FileEquals) == 0 {
				t.Error("check.file_equals must be present (objective grading)")
			}

			if goIndices[i] {
				assertExpandedGoCheck(t, s)
			} else {
				// S3：needle 场景不使用 Exec，靠文件存在与逐字节相等判定。
				if s.Check.AllowExec || s.Check.Exec != nil {
					t.Error("S3 must not enable exec")
				}
				if len(s.Check.FilesExist) == 0 {
					t.Error("S3 must declare files_exist")
				}
				if got := s.Check.FileEquals["answer.txt"]; got != "alpha-42\n" {
					t.Errorf("S3 answer.txt FileEquals = %q, want %q", got, "alpha-42\n")
				}
			}
		})
	}
}

// assertExpandedGoCheck 校验一个 Go 场景的客观判据：
// allow_exec + go test ./...(want 0) + file_equals，且 file_equals 锁死一份 *_test.go 种子内容。
func assertExpandedGoCheck(t *testing.T, s Scenario) {
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

	// 必须有一份 *_test.go 被逐字节锁死，且值就是种子里的原内容。
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

// TestCompletionExpandedNegativeSharesSeedAndCheck 锁定 S5 负对照与 S2 的契约：
// 同种子（files）、同 Check，只有脚本/期望不同，因此二者结果差异只能来自是否真的改对了文件。
func TestCompletionExpandedNegativeSharesSeedAndCheck(t *testing.T) {
	scenarios, err := Load(expandedFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion_expanded.json): %v", err)
	}
	s2, s5 := scenarios[1], scenarios[4]
	if !reflect.DeepEqual(s2.Files, s5.Files) {
		t.Error("S5 must share S2's seed files")
	}
	if !reflect.DeepEqual(s2.Check, s5.Check) {
		t.Error("S5 must share S2's Check byte-for-byte")
	}
	if reflect.DeepEqual(s2.Provider, s5.Provider) {
		t.Error("S5 script must differ from S2 (it fixes only one bug)")
	}
}
