package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// 本文件守护 evals/core.json 的真实场景数与 docs/STATUS.md 中
// 「core.json N 场景 / N 个脚本场景」口径的一致性，防止 todolist #91 P0 那类
// 漂移再次出现：eval_test.go 已断言 core.json 有 11 场景，文档却仍写 10。
// 它不运行任何场景，只做交叉核对——N 从 JSON 源码解析得出，文档数字用正则
// 抽取后逐一比对，任意一处不一致即变红。

// coreDocScenarioCountRe 抽取紧跟在 core.json 之后的场景数声明，覆盖文档里
// 实际出现的三种写法：
//   - `evals/core.json` 11 个脚本场景
//   - `evals/core.json`（11 场景）
//   - core.json 11 场景
//
// 用 [^。\n] 把匹配限制在同一句、同一行内，非贪婪量词确保取到 core.json 之后
// 的第一个「数字 + 场景」组合，避免跨句误抓别的数字。
var coreDocScenarioCountRe = regexp.MustCompile(`core\.json[^。\n]{0,32}?([0-9]+)\s*个?\s*(?:脚本)?场景`)

// coreScenariosCount 从 evals/core.json 解析出 scenarios 数组长度。
func coreScenariosCount(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "evals", "core.json"))
	if err != nil {
		t.Fatalf("读取 evals/core.json 失败: %v", err)
	}
	var parsed struct {
		Scenarios []json.RawMessage `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("解析 evals/core.json 失败: %v", err)
	}
	return len(parsed.Scenarios)
}

// TestCoreDocScenarioCountMatchesSource 核对 STATUS.md 中所有关于
// core.json 场景数的表述都等于 JSON 的真实数量。改了 core.json 的场景数却没同步
// 文档，或反过来改错了文档数字，都会在这里变红。
func TestCoreDocScenarioCountMatchesSource(t *testing.T) {
	want := coreScenariosCount(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "STATUS.md"))
	if err != nil {
		t.Fatalf("读取 docs/STATUS.md 失败: %v", err)
	}
	matches := coreDocScenarioCountRe.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		t.Fatal("docs/STATUS.md 未找到任何 core.json 场景数表述，守卫可能已失效")
	}
	for _, m := range matches {
		got, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("无法解析场景数 %q: %v", m[1], err)
		}
		if got != want {
			t.Errorf("场景数表述 %q 与 core.json 实际 %d 不一致（文档写 %d）", m[0], want, got)
		}
	}
}

// TestCoreScenariosCountIsEleven 把源码场景数钉死在 eval_test.go 已断言的 11，
// 作为双保险：即使文档被一起改错，也能独立发现 core.json 被误删或误加场景。
func TestCoreScenariosCountIsEleven(t *testing.T) {
	if got := coreScenariosCount(t); got != 11 {
		t.Fatalf("core.json 场景数 = %d, want 11（与 eval_test.go 的断言一致）", got)
	}
}
