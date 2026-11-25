package app

import (
	"path/filepath"
	"reflect"
	"testing"

	"example.com/argentgo/internal/cli"
)

// testTUICommandInfo 在隔离的工作区/主目录下调用 app 侧映射函数，
// 返回按命令名索引的描述与参数提示，便于断言补全列表的内容。
func testTUICommandInfo(t *testing.T, res Resolved, env func(string) string) map[string]struct {
	description  string
	argumentHint string
} {
	t.Helper()
	infos := buildTUICommandInfo(cli.IO{Env: env}, res)
	out := make(map[string]struct {
		description  string
		argumentHint string
	}, len(infos))
	for _, info := range infos {
		out[info.Command] = struct {
			description  string
			argumentHint string
		}{description: info.Description, argumentHint: info.ArgumentHint}
	}
	return out
}

// TestTUICommandInfoTrustGateAndSources 验证 TUI 补全面板与 /context 看到同一套
// 命令来源：内置命令与 markdown 命令始终出现；用户插件命令始终出现；
// 项目插件命令只在 TrustProject 时可见，否则不得泄漏进补全列表。
func TestTUICommandInfoTrustGateAndSources(t *testing.T) {
	for _, tc := range []struct {
		name  string
		trust bool
	}{
		{name: "untrusted", trust: false},
		{name: "trusted", trust: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, root, home := pluginWiringEnv(t, tc.trust)

			// 用户安装的插件命令：始终可用。
			userDir := filepath.Join(home, ".argentgo", "plugins", "utility")
			writeFile(t, filepath.Join(userDir, "plugin.json"),
				`{"name":"utility","commands":["commands"]}`)
			writeFile(t, filepath.Join(userDir, "commands", "hello.md"), userCommandDoc)

			// 仓库附带的插件命令：受 TrustProject 门控。
			projectDir := filepath.Join(root, ".argent", "plugins", "evil")
			writeFile(t, filepath.Join(projectDir, "plugin.json"),
				`{"name":"evil","commands":["commands"]}`)
			writeFile(t, filepath.Join(projectDir, "commands", "pwn.md"), projectCommandDoc)

			// 项目级与用户级 markdown 命令。
			writeFile(t, filepath.Join(root, ".argent", "commands", "deploy.md"),
				"---\ndescription: deploy project\nargument-hint: <env>\n---\nDeploy $ARGUMENTS\n")
			writeFile(t, filepath.Join(home, ".argentgo", "commands", "greet.md"),
				"---\ndescription: greet user\n---\nHello $ARGUMENTS\n")

			env := envFrom(map[string]string{"HOME": home, "USERPROFILE": home})
			byName := testTUICommandInfo(t, res, env)

			// 内置命令 /context、/review 必须出现。
			for _, name := range []string{"context", "review"} {
				if _, ok := byName[name]; !ok {
					t.Fatalf("builtin /%s missing from TUI CommandInfo: %v", name, byName)
				}
			}

			// markdown 命令出现，且描述与参数提示被正确映射。
			deploy, ok := byName["deploy"]
			if !ok {
				t.Fatalf("project markdown command missing: %v", byName)
			}
			if deploy.description != "deploy project" || deploy.argumentHint != "<env>" {
				t.Fatalf("deploy summary not mapped: %+v", deploy)
			}
			greet, ok := byName["greet"]
			if !ok || greet.description != "greet user" {
				t.Fatalf("user markdown command missing or wrong: %+v, ok=%v", greet, ok)
			}

			// 用户插件命令始终出现（命名空间前缀保留）。
			if _, ok := byName["utility:hello"]; !ok {
				t.Fatalf("user plugin command missing: %v", byName)
			}

			// 项目插件命令：仅受信任时出现，不受信任时不得泄漏。
			_, projectFound := byName["evil:pwn"]
			if tc.trust && !projectFound {
				t.Fatal("trusted project plugin command missing from TUI CommandInfo")
			}
			if !tc.trust && projectFound {
				t.Fatal("untrusted project plugin command leaked into TUI CommandInfo")
			}
		})
	}
}

// TestTUICommandInfoDoesNotMutateInputs 验证映射函数是纯投影：
// 重复调用结果一致，且不修改传入的 Resolved（Root / TrustProject / Plugins）。
func TestTUICommandInfoDoesNotMutateInputs(t *testing.T) {
	res, root, home := pluginWiringEnv(t, true)
	writeFile(t, filepath.Join(root, ".argent", "commands", "deploy.md"),
		"---\ndescription: deploy project\n---\nDeploy $ARGUMENTS\n")
	env := envFrom(map[string]string{"HOME": home, "USERPROFILE": home})

	before := Resolved{
		Root:         res.Root,
		TrustProject: res.TrustProject,
		Plugins:      append([]string(nil), res.Plugins...),
	}

	first := buildTUICommandInfo(cli.IO{Env: env}, res)
	second := buildTUICommandInfo(cli.IO{Env: env}, res)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("buildTUICommandInfo is not idempotent:\nfirst:  %v\nsecond: %v", first, second)
	}
	if res.Root != before.Root || res.TrustProject != before.TrustProject || !reflect.DeepEqual(res.Plugins, before.Plugins) {
		t.Fatalf("buildTUICommandInfo mutated its Resolved argument: got %+v, want %+v", res, before)
	}
}
