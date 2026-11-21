package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本文件守护 docs/OPT_INS.md、独立门控登记表（optin_metadata.go）与源码读点
// 三者的一致性，防止「代码加了门控却忘了写文档」以及「文档写了不存在的门控」。
// 扫描刻意保守：只匹配 ARGENT_ 前缀的明确门控变量，且源码只读非 _test.go 的
// 字符串字面量（真实读点），不把注释、测试辅助变量和其它 env 误算进来。

// argentEnvToken 匹配文档中的 ARGENT_ 变量名（明确前缀，避免误伤其它 env）。
var argentEnvToken = regexp.MustCompile(`ARGENT_[A-Z][A-Z0-9_]*`)

// argentEnvLiteral 匹配源码中带引号的 ARGENT_ 字符串字面量，即真实读点。
var argentEnvLiteral = regexp.MustCompile(`"ARGENT_[A-Z][A-Z0-9_]*"`)

// repoRoot 从当前包目录向上找到 go.mod，作为文档与源码扫描的根。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("向上遍历未找到 go.mod")
		}
		dir = parent
	}
}

// documentedOptInEnvs 返回 OPT_INS.md 中出现过的全部 ARGENT_ 变量名。
func documentedOptInEnvs(t *testing.T) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "OPT_INS.md"))
	if err != nil {
		t.Fatalf("读取 docs/OPT_INS.md 失败: %v", err)
	}
	out := make(map[string]bool)
	for _, token := range argentEnvToken.FindAllString(string(data), -1) {
		out[token] = true
	}
	return out
}

// sourceReadEnvs 返回源码（非测试文件）字符串字面量中出现过的 ARGENT_ 变量名。
// 只扫描 internal / cmd / native 三类目录，跳过 _test.go 与 vendor。
func sourceReadEnvs(t *testing.T) map[string]bool {
	t.Helper()
	root := repoRoot(t)
	out := make(map[string]bool)
	for _, base := range []string{"internal", "cmd", "native"} {
		baseDir := filepath.Join(root, base)
		if _, err := os.Stat(baseDir); err != nil {
			continue
		}
		err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, lit := range argentEnvLiteral.FindAllString(string(data), -1) {
				out[strings.Trim(lit, `"`)] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("扫描源码目录 %s 失败: %v", base, err)
		}
	}
	return out
}

// TestIndependentOptInsUnique 保证登记表的 Env 与 Key 均无重复。
func TestIndependentOptInsUnique(t *testing.T) {
	envs := make(map[string]bool)
	keys := make(map[string]bool)
	for _, item := range IndependentOptIns {
		if item.Env == "" || item.Key == "" {
			t.Errorf("登记表存在空 Env/Key: %+v", item)
			continue
		}
		if envs[item.Env] {
			t.Errorf("登记表 Env 重复: %s", item.Env)
		}
		envs[item.Env] = true
		if keys[item.Key] {
			t.Errorf("登记表 Key 重复: %s", item.Key)
		}
		keys[item.Key] = true
	}
}

// TestIndependentOptInsDocumented 守护「表中有、文档缺」方向：
// 每个登记的环境变量都必须在 OPT_INS.md 中有条目。
func TestIndependentOptInsDocumented(t *testing.T) {
	doc := documentedOptInEnvs(t)
	for _, item := range IndependentOptIns {
		if !doc[item.Env] {
			t.Errorf("独立门控 %s（%s）未在 docs/OPT_INS.md 中记载", item.Env, item.Name)
		}
	}
}

// TestIndependentOptInsHaveSourceReadPoints 守护登记表本身不过时：
// 除明确标记 Historical 的历史占位外，每个门控都要在源码里有真实读点。
func TestIndependentOptInsHaveSourceReadPoints(t *testing.T) {
	src := sourceReadEnvs(t)
	for _, item := range IndependentOptIns {
		if item.Historical {
			continue
		}
		if !src[item.Env] {
			t.Errorf("独立门控 %s 在源码非测试文件中没有读点（可能已失效或拼写错误）", item.Env)
		}
	}
}

// TestDocsOptInsNoGhost 守护「文档有、源码/表皆无」方向：
// 文档里的 ARGENT_ 变量必须要么已登记、要么有真实源码读点，
// ARGENT_OPT_INS 是承载统一列表的变量，单独豁免。
func TestDocsOptInsNoGhost(t *testing.T) {
	doc := documentedOptInEnvs(t)
	src := sourceReadEnvs(t)
	known := map[string]bool{optInEnvVar: true}
	for _, item := range IndependentOptIns {
		known[item.Env] = true
	}

	var ghosts []string
	for env := range doc {
		if known[env] || src[env] {
			continue
		}
		ghosts = append(ghosts, env)
	}
	sort.Strings(ghosts)
	if len(ghosts) > 0 {
		t.Fatalf("docs/OPT_INS.md 存在无源码读点且未登记的幽灵门控: %v", ghosts)
	}
}
