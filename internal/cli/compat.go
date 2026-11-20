package cli

import (
	"fmt"
	"sort"
	"strings"
)

// unsupportedFlag 报告第一个其子系统尚未迁移的 flag。消息只有一行，
// 便于脚本捕获，且 runMain 会以 1 退出：flag 绝不会被静默忽略。
func unsupportedFlag(opts *Options) string {
	switch {
	case opts.Worktree != "":
		return "--worktree is not migrated yet: create the worktree yourself, then pass it with -C"
	case opts.Background:
		return "--background is not migrated yet: run argent under your process supervisor"
	case opts.EnableBrowser:
		return "--enable-browser is not migrated yet: the browser tool is not ported"
	case opts.EnableDesktop:
		return "--enable-desktop is not migrated yet: desktop control is not ported"
	case opts.EnableService:
		return "--enable-service is not migrated yet: the pty service tool is not ported"
	case opts.WorkflowMode != "":
		return "--workflow-mode is not migrated yet: drive internal/workflow from your own program"
	case opts.WorkflowResume:
		return "--workflow-resume is not migrated yet: workflow journals are not ported"
	case opts.WorkflowJournal != "":
		return "--workflow-journal is not migrated yet: workflow journals are not ported"
	case opts.GUI:
		return "--gui is not migrated yet: the desktop shell is a later wave; use `argent tui`"
	}
	return ""
}

// CLI 兼容性说明只针对那些仍能解析但无法生效的 flag。
// 当某个子系统落地时，必须在此移除其说明（并更新对应测试）。
func (a App) warnPartial(opts *Options) {
	if strings.TrimSpace(opts.OutputSchema) != "" {
		fmt.Fprintln(a.IO.Err, "note: --output-schema validates the final answer and retries once")
	}
}

// listProfiles 实现 --list-profiles。当没有注入 lister 时，
// 它读取配置文件中嵌入的 profiles。
func (a App) listProfiles(opts Options) int {
	var (
		names  []string
		active string
		err    error
	)
	if a.Profiles != nil {
		names, active, err = a.Profiles(opts)
	} else {
		names, err = configProfiles(a.configPath())
	}
	if err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1
	}
	if len(names) == 0 {
		fmt.Fprintln(a.IO.Out, "no profiles configured")
		return 0
	}
	sort.Strings(names)
	for _, name := range names {
		marker := "  "
		if name == active {
			marker = "* "
		}
		fmt.Fprintln(a.IO.Out, marker+name)
	}
	return 0
}

// configProfiles 从单个配置文件读取 `profiles` 对象。
func configProfiles(path string) ([]string, error) {
	cfg, err := loadConfigFile(path)
	if err != nil {
		return nil, err
	}
	profiles, _ := cfg["profiles"].(map[string]any)
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	return names, nil
}

// printTokens 实现 --tokens。
func (a App) printTokens(opts Options) int {
	if a.TokenReport == nil {
		fmt.Fprintln(a.IO.Err, "error: token report not configured")
		return 1
	}
	report, err := a.TokenReport(opts)
	if err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprint(a.IO.Out, report)
	return 0
}
