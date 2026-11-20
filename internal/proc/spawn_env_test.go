package proc

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// envProbeMarker 把重新执行的测试二进制变成一个小型 env 转储器。
const envProbeMarker = "ARGENT_PROC_ENV_PROBE"

// envProbeKeys 是辅助进程报告的固定变量集。宿主私有
// 键不带后缀，这样精确名称和前缀黑名单都能覆盖到。
var envProbeKeys = []string{
	"ARGENT_SESSION_ID",
	"ARGENT_RUN_ID",
	"ARGENT_PARENT_X",
	"SUBAGENT_PARENT_X",
	"PROC_ENV_NORMAL",
	"PROC_ENV_EXTRA",
}

// TestProcEnvProbeHelper 在正常运行中是空操作。生成测试会带着标记重新执行
// 测试二进制；辅助进程打印它看到的值，
// 使父进程可以断言子进程究竟继承了哪些变量。
func TestProcEnvProbeHelper(t *testing.T) {
	if os.Getenv(envProbeMarker) == "" {
		return
	}
	for _, key := range envProbeKeys {
		_, _ = fmt.Fprintf(os.Stdout, "PROBE:%s=%s\n", key, os.Getenv(key))
	}
}

func envProbeCommand(extra []string) Command {
	env := append([]string{envProbeMarker + "=1"}, extra...)
	return Command{
		Path: os.Args[0],
		Args: []string{"-test.run=^TestProcEnvProbeHelper$"},
		Env:  env,
	}
}

// parseEnvProbe 只保留辅助进程自己的行；测试框架还会向 stdout
// 写入 "PASS"/"ok" 行。
func parseEnvProbe(out string) map[string]string {
	got := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "PROBE:") {
			continue
		}
		key, value, _ := strings.Cut(strings.TrimPrefix(line, "PROBE:"), "=")
		got[key] = value
	}
	return got
}

// TestSpawnPathsFilterHostPrivateEnv 检查包中每条生成路径
// 都给子进程相同的净化环境。Run 一直在过滤宿主私有变量；
// 分离路径（RunWithTimeout 用于提升后台作业，
// 即嵌套 agent）也必须如此，否则嵌套 agent 会把宿主的
// 会话管道误认为自己的。
func TestSpawnPathsFilterHostPrivateEnv(t *testing.T) {
	const (
		sessionVal   = "sentinel-session-9f3a"
		runVal       = "sentinel-run-7c1b"
		parentVal    = "sentinel-parent-2d8e"
		subParentVal = "sentinel-subparent-4a6f"
		normalVal    = "sentinel-normal-5b2c"
		extraVal     = "sentinel-extra-1c7e"
		extraPrivVal = "sentinel-extra-private-3e9d"
	)

	spawns := []struct {
		name string
		run  func(Command) (string, error)
	}{
		{
			name: "Run",
			run: func(c Command) (string, error) {
				res, err := Run(context.Background(), c)
				return res.Stdout, err
			},
		},
		{
			name: "StartDetached",
			run: func(c Command) (string, error) {
				p, err := StartDetached(c)
				if err != nil {
					return "", err
				}
				_ = p.Wait()
				return p.Output(), nil
			},
		},
	}

	tests := []struct {
		name     string
		setEnv   map[string]string
		extra    []string
		wantGone map[string]string // 绝不能到达子进程的 key -> value
		wantKept map[string]string // 必须到达子进程的 key -> value
	}{
		{
			name: "exact and prefix host-private keys are filtered",
			setEnv: map[string]string{
				"ARGENT_SESSION_ID": sessionVal,
				"ARGENT_RUN_ID":     runVal,
				"ARGENT_PARENT_X":   parentVal,
				"SUBAGENT_PARENT_X": subParentVal,
				"PROC_ENV_NORMAL":   normalVal,
			},
			wantGone: map[string]string{
				"ARGENT_SESSION_ID": sessionVal,
				"ARGENT_RUN_ID":     runVal,
				"ARGENT_PARENT_X":   parentVal,
				"SUBAGENT_PARENT_X": subParentVal,
			},
			wantKept: map[string]string{"PROC_ENV_NORMAL": normalVal},
		},
		{
			name:   "explicit extra entries are never filtered",
			setEnv: map[string]string{"PROC_ENV_NORMAL": normalVal},
			extra: []string{
				"PROC_ENV_EXTRA=" + extraVal,
				"ARGENT_SESSION_ID=" + extraPrivVal,
			},
			wantGone: map[string]string{"ARGENT_SESSION_ID": sessionVal},
			wantKept: map[string]string{
				"PROC_ENV_NORMAL":   normalVal,
				"PROC_ENV_EXTRA":    extraVal,
				"ARGENT_SESSION_ID": extraPrivVal,
			},
		},
	}

	for _, sp := range spawns {
		for _, tc := range tests {
			t.Run(sp.name+"/"+tc.name, func(t *testing.T) {
				for k, v := range tc.setEnv {
					t.Setenv(k, v)
				}
				out, err := sp.run(envProbeCommand(tc.extra))
				if err != nil {
					t.Fatalf("spawn: %v", err)
				}
				got := parseEnvProbe(out)
				for key, val := range tc.wantGone {
					if got[key] == val {
						t.Errorf("host-private %s=%q leaked into child", key, val)
					}
				}
				for key, val := range tc.wantKept {
					if got[key] != val {
						t.Errorf("child %s = %q, want %q", key, got[key], val)
					}
				}
			})
		}
	}
}
