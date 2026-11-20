package app

import (
	"bytes"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/commands"
	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/hooks"
)

// pluginWiringEnv 隔离工作区和进程主目录，使插件发现只看到测试放置的包。
// plugins.Discover 通过 os.UserHomeDir 解析用户目录，
// 因此两个环境变量都被设置。
func pluginWiringEnv(t *testing.T, trust bool) (Resolved, string, string) {
	t.Helper()
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	env := envFrom(map[string]string{"HOME": home, "USERPROFILE": home})
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root, TrustProject: trust})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return res, root, home
}

const userCommandDoc = "---\ndescription: user plugin hello\n---\nSay hello $ARGUMENTS\n"
const projectCommandDoc = "---\ndescription: project plugin pwn\n---\nPWN $ARGUMENTS\n"
const userHookDoc = `{"hooks":{"SessionStart":[{"hooks":[{"command":"echo user-hook"}]}]}}`
const projectHookDoc = `{"hooks":{"SessionStart":[{"hooks":[{"command":"echo project-hook"}]}]}}`

func TestPluginCommandsRespectTrustGate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		trust bool
	}{
		{name: "untrusted", trust: false},
		{name: "trusted", trust: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, root, home := pluginWiringEnv(t, tc.trust)

			// 用户安装的命令：始终可用。
			userDir := filepath.Join(home, ".argentgo", "plugins", "utility")
			writeFile(t, filepath.Join(userDir, "plugin.json"),
				`{"name":"utility","commands":["commands"]}`)
			writeFile(t, filepath.Join(userDir, "commands", "hello.md"), userCommandDoc)

			// 仓库附带的命令：受 TrustProject 门控。
			projectDir := filepath.Join(root, ".argent", "plugins", "evil")
			writeFile(t, filepath.Join(projectDir, "plugin.json"),
				`{"name":"evil","commands":["commands"]}`)
			writeFile(t, filepath.Join(projectDir, "commands", "pwn.md"), projectCommandDoc)

			sources := pluginCommandSources(res)
			set := commands.NewSetWithBuiltins(
				append(commands.DefaultSources(root, home), sources...))

			cmd, ok := set.Get("utility:hello")
			if !ok || cmd.Plugin != "utility" || cmd.Name != "utility:hello" {
				t.Fatalf("user plugin command missing or not namespaced: %+v, ok=%v", cmd, ok)
			}
			if got, err := cmd.Expand("world"); err != nil || !strings.Contains(got, "hello world") {
				t.Fatalf("plugin command did not expand: %q, err=%v", got, err)
			}

			_, projectFound := set.Get("evil:pwn")
			if tc.trust && !projectFound {
				t.Fatal("trusted project plugin command was not discovered")
			}
			if !tc.trust && projectFound {
				t.Fatal("untrusted project plugin command leaked into the set")
			}

			// app 层的组装点（/context）必须看到同一集合。
			wantCount := 3 // 2 个内置命令 + 用户插件命令
			if tc.trust {
				wantCount = 4
			}
			env := envFrom(map[string]string{"HOME": home, "USERPROFILE": home})
			report, ok := contextPrompt(res, cli.IO{Env: env}, "/context")
			if !ok || !strings.Contains(report, "commands: "+strconv.Itoa(wantCount)) {
				t.Fatalf("/context command count wrong (want %d):\n%s", wantCount, report)
			}
		})
	}
}

func TestPluginCommandEscapingPathRefused(t *testing.T) {
	res, root, _ := pluginWiringEnv(t, true)
	outside := t.TempDir()
	dir := filepath.Join(root, ".argent", "plugins", "escaping")
	body := `{"name":"escaping","commands":[` + strconv.Quote(filepath.ToSlash(outside)) + `]}`
	writeFile(t, filepath.Join(dir, "plugin.json"), body)

	for _, source := range pluginCommandSources(res) {
		if source.Root == outside || source.Prefix == "escaping" {
			t.Fatalf("escaping manifest path became a command source: %+v", source)
		}
	}
}

func TestPluginHookPathsRespectTrustGate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		trust bool
	}{
		{name: "untrusted", trust: false},
		{name: "trusted", trust: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, root, home := pluginWiringEnv(t, tc.trust)

			userDir := filepath.Join(home, ".argentgo", "plugins", "hookuser")
			writeFile(t, filepath.Join(userDir, "plugin.json"),
				`{"name":"hookuser","hooks":{"paths":["hooks/user.json"]}}`)
			userHook := filepath.Join(userDir, "hooks", "user.json")
			writeFile(t, userHook, userHookDoc)

			projectDir := filepath.Join(root, ".argent", "plugins", "hookproj")
			writeFile(t, filepath.Join(projectDir, "plugin.json"),
				`{"name":"hookproj","hooks":{"paths":["hooks/project.json"]}}`)
			projectHook := filepath.Join(projectDir, "hooks", "project.json")
			writeFile(t, projectHook, projectHookDoc)

			paths := pluginHookPaths(res)
			joined := strings.Join(paths, "\n")
			if !strings.Contains(joined, filepath.ToSlash(userHook)) && !strings.Contains(joined, userHook) {
				t.Fatalf("user plugin hook path missing: %v", paths)
			}
			if tc.trust && !strings.Contains(joined, projectHook) {
				t.Fatalf("trusted project plugin hook path missing: %v", paths)
			}
			if !tc.trust && strings.Contains(joined, projectHook) {
				t.Fatalf("untrusted project plugin hook path leaked: %v", paths)
			}
		})
	}
}

func TestLoadHookDocumentsLoadsTrustedPluginHook(t *testing.T) {
	for _, tc := range []struct {
		name  string
		trust bool
	}{
		{name: "untrusted", trust: false},
		{name: "trusted", trust: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, root, home := pluginWiringEnv(t, tc.trust)

			userDir := filepath.Join(home, ".argentgo", "plugins", "hookuser")
			writeFile(t, filepath.Join(userDir, "plugin.json"),
				`{"name":"hookuser","hooks":{"paths":["hooks/user.json"]}}`)
			userHook := filepath.Join(userDir, "hooks", "user.json")
			writeFile(t, userHook, userHookDoc)

			projectDir := filepath.Join(root, ".argent", "plugins", "hookproj")
			writeFile(t, filepath.Join(projectDir, "plugin.json"),
				`{"name":"hookproj","hooks":{"paths":["hooks/project.json"]}}`)
			projectHook := filepath.Join(projectDir, "hooks", "project.json")
			writeFile(t, projectHook, projectHookDoc)

			// 批准两个摘要：剩下的唯一门槛是对哪些路径交给加载器的信任门控。
			// （接上行）
			trust := hooks.NewTrustStore()
			if _, err := trust.TrustFile(userHook); err != nil {
				t.Fatal(err)
			}
			if _, err := trust.TrustFile(projectHook); err != nil {
				t.Fatal(err)
			}
			res.Storage = t.TempDir()
			res.Cfg = config.Config{Raw: map[string]any{}}

			config := loadHookDocuments(res, trust, &bytes.Buffer{})
			if config == nil {
				t.Fatal("no hook config loaded")
			}
			if !hookCommandPresent(config, "echo user-hook") {
				t.Fatal("trusted user plugin hook was not loaded")
			}
			projectPresent := hookCommandPresent(config, "echo project-hook")
			if tc.trust && !projectPresent {
				t.Fatal("trusted project plugin hook was not loaded")
			}
			if !tc.trust && projectPresent {
				t.Fatal("untrusted project plugin hook was loaded")
			}
		})
	}
}

func hookCommandPresent(config *hooks.Config, command string) bool {
	for _, entry := range config.Commands {
		if strings.Contains(entry.Command, command) {
			return true
		}
	}
	return false
}
