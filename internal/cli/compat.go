package cli

import (
	"fmt"
	"sort"
	"strings"
)

// unsupportedFlag reports the first flag whose subsystem is not migrated. The
// message is one line so a script can surface it, and runMain exits 1: a flag
// is never silently ignored.
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

// warnPartial documents the two flags that parse and resolve but cannot change
// behaviour because a package this slice may not edit has no field for them.
func (a App) warnPartial(opts *Options) {
	if strings.TrimSpace(opts.SecondaryModel) != "" {
		fmt.Fprintln(a.IO.Err, "note: --secondary-model is carried but has no effect yet (internal/agent has no subagent model field)")
	}
	if len(opts.AddDirs) > 0 {
		fmt.Fprintln(a.IO.Err, "note: --add-dir extends the sandbox write roots; the tool registry still resolves paths inside --cwd only")
	}
	if strings.TrimSpace(opts.Theme) != "" || opts.NoColor {
		fmt.Fprintln(a.IO.Err, "note: --theme/--no-color are carried but the TUI has no theme switch yet")
	}
	if opts.NoAutoCompact {
		fmt.Fprintln(a.IO.Err, "note: --no-auto-compact is carried but automatic compaction is not wired into a run yet")
	}
}

// listProfiles implements --list-profiles. It reads the profiles embedded in the
// config file when no lister is injected.
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

// configProfiles reads the `profiles` object from one config file.
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

// printTokens implements --tokens.
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
