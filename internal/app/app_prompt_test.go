package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/config"
)

// TestStaticSystemIgnoresProcessContext 证明缓存稳定的前缀不嵌入进程特定状态。
// 从不同工作目录进行的两次构建（在没有本应不同的项目文档或技能时）必须逐字节一致，
// 且都不能提及 cwd 或推理强度。
// （接上行）
func TestStaticSystemIgnoresProcessContext(t *testing.T) {
	env, rootA, _ := wiringEnv(t)
	rootB := t.TempDir()

	staticA := buildStaticSystem(rootA, env, true)
	staticB := buildStaticSystem(rootB, env, true)
	if staticA != staticB {
		t.Fatalf("static system drifted with cwd:\nA: %q\nB: %q", staticA, staticB)
	}
	for _, leaked := range []string{"Working directory", "Reasoning effort", "JSON Schema"} {
		if strings.Contains(staticA, leaked) {
			t.Fatalf("static system leaked process context %q: %q", leaked, staticA)
		}
	}
}

// TestDynamicContextCarriesProcessContext 证明拆分不会丢失信息：动态那一半仍携带 cwd、推理强度
// 和输出 schema，并使用原始组合提示的确切措辞。
// （接上行）
func TestDynamicContextCarriesProcessContext(t *testing.T) {
	root := t.TempDir()
	const schema = `{"type":"object","properties":{"ok":{"type":"boolean"}}}`
	dynamic := buildDynamicContext(root, "high", schema)
	for _, want := range []string{
		"Working directory: " + root,
		"Reasoning effort: high. Spend accordingly.",
		"Reply with a single JSON document that conforms to this JSON Schema:",
		schema,
	} {
		if !strings.Contains(dynamic, want) {
			t.Fatalf("dynamic context missing %q: %q", want, dynamic)
		}
	}
}

// TestCachePrefixStableAcrossProcessChanges 证明提供方缓存的字节（静态系统前缀）在 cwd 或思考级别改变时
// 保持不变，而动态值仍通过组合提示到达请求。这正是 dsh 风格拆分所依赖的不变量。
// （接上行）
// （接上行）
func TestCachePrefixStableAcrossProcessChanges(t *testing.T) {
	env, rootA, _ := wiringEnv(t)
	rootB := t.TempDir()

	prefixA := buildStaticSystem(rootA, env, true)
	prefixB := buildStaticSystem(rootB, env, true)
	fullA := buildSystemWithTrust(rootA, env, true, "low", "")
	fullB := buildSystemWithTrust(rootB, env, true, "high", "")

	if prefixA != prefixB {
		t.Fatalf("cache prefix changed across cwd/thinking:\nA: %q\nB: %q", prefixA, prefixB)
	}
	for _, full := range []string{fullA, fullB} {
		if !strings.HasPrefix(full, prefixA) {
			t.Fatalf("combined system no longer starts with the static prefix:\nfull: %q\nprefix: %q", full, prefixA)
		}
	}
	if !strings.Contains(fullA, "Reasoning effort: low") || !strings.Contains(fullB, "Reasoning effort: high") {
		t.Fatalf("dynamic reasoning effort lost from combined prompt:\nA: %q\nB: %q", fullA, fullB)
	}
	if !strings.Contains(fullB, "Working directory: "+rootB) {
		t.Fatalf("dynamic cwd lost from combined prompt: %q", fullB)
	}
}

// TestBuildSystemWithTrustKeepsDynamicInfo 守护旧版组合：现有调用方使用的完整系统字符串
// 在重构后仍必须包含每个字段，使任何现有提示断言的语义都不改变。
// （接上行）
func TestBuildSystemWithTrustKeepsDynamicInfo(t *testing.T) {
	env, root, _ := wiringEnv(t)
	const schema = `{"type":"object"}`
	full := buildSystemWithTrust(root, env, true, "medium", schema)
	for _, want := range []string{
		"Working directory: " + root,
		"Reasoning effort: medium. Spend accordingly.",
		"Reply with a single JSON document that conforms to this JSON Schema:",
		schema,
	} {
		if !strings.Contains(full, want) {
			t.Fatalf("combined system missing %q: %q", want, full)
		}
	}
}

// TestPlatformFactsInStaticSystem 证明缓存稳定的前缀在仅静态和组合式两种构建器中都携带运行时操作系统/架构和推断出的 shell。
// COMSPEC 使用裸的基本名称，使断言在所有宿主操作系统上都成立。
// （接上行）
// （接上行）
func TestPlatformFactsInStaticSystem(t *testing.T) {
	_, root, home := wiringEnv(t)
	factsEnv := envFrom(map[string]string{
		"USERPROFILE": home,
		"HOME":        home,
		"COMSPEC":     "cmd.exe",
	})
	wantPlatform := fmt.Sprintf("Platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	for name, system := range map[string]string{
		"static":   buildStaticSystem(root, factsEnv, true),
		"composed": buildSystem(root, factsEnv, "", ""),
	} {
		for _, want := range []string{wantPlatform, "Shell: cmd.exe"} {
			if !strings.Contains(system, want) {
				t.Errorf("%s system missing %q: %q", name, want, system)
			}
		}
	}
}

// TestShellFallsBackToSHELL 证明 COMSPEC 不是必需的：在 Unix 上 SHELL 变量提供 shell 名称。
// （接上行）
func TestShellFallsBackToSHELL(t *testing.T) {
	env := envFrom(map[string]string{"SHELL": "bash"})
	system := buildStaticSystem(t.TempDir(), env, true)
	if !strings.Contains(system, "Shell: bash") {
		t.Fatalf("static system missing SHELL fallback: %q", system)
	}
}

// TestVerifyGuidanceOnlyWhenConfigured 证明默认提示字节不受验证配置影响：未配置任何内容时不存在，
// 一旦任一字段被设置，就出现一条不要重复的提示。
// （接上行）
func TestVerifyGuidanceOnlyWhenConfigured(t *testing.T) {
	env, root, _ := wiringEnv(t)

	if base := buildSystem(root, env, "", ""); strings.Contains(base, "Verification:") {
		t.Fatalf("default prompt leaked verify guidance: %q", base)
	}

	withCmd := buildSystem(root, env, "", "", config.Config{
		VerifyCommand: []string{"go", "test", "./..."},
	})
	for _, want := range []string{"Verification:", "go test ./...", "do not suggest re-running them"} {
		if !strings.Contains(withCmd, want) {
			t.Errorf("VerifyCommand prompt missing %q: %q", want, withCmd)
		}
	}

	withMap := buildSystem(root, env, "", "", config.Config{
		VerifyCommands: map[string]string{".go": "go vet ./...", "py": "pytest -q"},
	})
	for _, want := range []string{"go vet ./...", "pytest -q", "do not suggest re-running them"} {
		if !strings.Contains(withMap, want) {
			t.Errorf("VerifyCommands prompt missing %q: %q", want, withMap)
		}
	}
}

// TestVerifyGuidanceDeterministic 证明 map 的迭代顺序不会使缓存稳定的前缀漂移：
// 以不同顺序插入相同条目的两份配置必须渲染出逐字节相同的提示。
// （接上行）
func TestVerifyGuidanceDeterministic(t *testing.T) {
	env, root, _ := wiringEnv(t)
	a := buildSystem(root, env, "", "", config.Config{
		VerifyCommands: map[string]string{".go": "go test ./...", "py": "pytest -q"},
	})
	b := buildSystem(root, env, "", "", config.Config{
		VerifyCommands: map[string]string{"py": "pytest -q", ".go": "go test ./..."},
	})
	if a != b {
		t.Fatalf("verify guidance drifted with map order:\nA: %q\nB: %q", a, b)
	}
}

// TestCWDGitDetection 证明仓库提示遵循工作区根目录：.git 目录或 .git 文件会标记它，普通目录则不会。
// （接上行）
func TestCWDGitDetection(t *testing.T) {
	env, _, _ := wiringEnv(t)

	plain := t.TempDir()
	if got := buildSystem(plain, env, "", ""); strings.Contains(got, "Repository:") {
		t.Fatalf("plain directory wrongly marked as a repository: %q", got)
	}
	const want = "Repository: this working directory is a git repository."

	dirRepo := t.TempDir()
	if err := os.Mkdir(filepath.Join(dirRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := buildSystem(dirRepo, env, "", ""); !strings.Contains(got, want) {
		t.Fatalf(".git directory not detected: %q", got)
	}

	fileRepo := t.TempDir()
	if err := os.WriteFile(filepath.Join(fileRepo, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := buildSystem(fileRepo, env, "", ""); !strings.Contains(got, want) {
		t.Fatalf(".git file not detected: %q", got)
	}
}
