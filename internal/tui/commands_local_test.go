package tui

import (
	"strings"
	"testing"
)

// 输入 `/` 时应看到 TUI 自己的命令，而不是只有宿主注入的两条内置命令。
func TestSetCommandInfoIncludesTUICommands(t *testing.T) {
	m := New(Config{Locale: "zh-CN"})
	m.SetCommandInfo([]CommandInfo{{Command: "context", Description: "English host text"}})

	got := make(map[string]string)
	for _, c := range m.visibleCommands() {
		got[c.Command] = c.Description
	}
	for _, want := range []string{"help", "clear", "expand", "undo", "retry", "name", "quit", "context"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("completion is missing /%s (got %v)", want, got)
		}
	}
	if got["help"] != "查看全部快捷键" {
		t.Fatalf("/help description = %q, want the Chinese catalog entry", got["help"])
	}
	if got["context"] != "只读查看模型上下文" {
		t.Fatalf("/context description = %q, want it localized rather than host English", got["context"])
	}
}

// 描述与参数提示都按 locale 取；目录里没登记的命令保留宿主原文，不猜。
func TestCommandDescriptionsFollowLocale(t *testing.T) {
	en := localCommandInfo("en-US", CommandInfo{Command: "undo"})
	if want := "revert the latest checkpoint on disk"; en.Description != want {
		t.Fatalf("en /undo description = %q, want %q", en.Description, want)
	}
	if want := "[--steps N]"; en.ArgumentHint != want {
		t.Fatalf("en /undo hint = %q, want %q", en.ArgumentHint, want)
	}

	zh := localCommandInfo("zh-CN", CommandInfo{Command: "undo"})
	if want := "回滚最近一次检查点的磁盘改动"; zh.Description != want {
		t.Fatalf("zh /undo description = %q, want %q", zh.Description, want)
	}

	unknown := localCommandInfo("zh-CN", CommandInfo{Command: "custom", Description: "host text"})
	if unknown.Description != "host text" {
		t.Fatalf("unregistered command description = %q, want the host text untouched", unknown.Description)
	}
}

// 宿主同名命令只出现一次，且以宿主提供的元数据为准。
func TestMergeCommandInfoKeepsOneCopyPerName(t *testing.T) {
	merged := mergeCommandInfo("en-US", []CommandInfo{{Command: "undo", ArgumentHint: "--host"}})
	count := 0
	for _, row := range merged {
		if row.Command != "undo" {
			continue
		}
		count++
		if row.ArgumentHint != "--host" {
			t.Fatalf("/undo hint = %q, want the host hint to win", row.ArgumentHint)
		}
	}
	if count != 1 {
		t.Fatalf("/undo appears %d times, want 1", count)
	}
}

// 注册表是新增命令的唯一入口：重名、缺执行体或缺 i18n 描述都会在
// 单测里暴露，避免补全里有命令却敲了没反应。
func TestNativeCommandRegistryInvariants(t *testing.T) {
	seen := make(map[string]struct{}, len(nativeCommands))
	for _, cmd := range nativeCommands {
		if cmd.Name == "" || cmd.Run == nil {
			t.Fatalf("incomplete command registration: %+v", cmd)
		}
		if _, dup := seen[cmd.Name]; dup {
			t.Fatalf("duplicate command /%s", cmd.Name)
		}
		seen[cmd.Name] = struct{}{}
		if _, ok := commandDescription("en-US", cmd.Name); !ok {
			t.Errorf("/%s has no catalog description", cmd.Name)
		}
	}
	for _, name := range tuiNativeCommands {
		if name == "exit" {
			t.Fatal("/exit is an alias and must not occupy a completion row")
		}
		if _, ok := seen[name]; !ok {
			t.Errorf("completion /%s is not backed by a registry entry", name)
		}
	}
}

// /exit 是 /quit 的别名，执行体必须一致。
func TestExitCommandIsQuitAlias(t *testing.T) {
	quit, ok := lookupNativeCommand("quit")
	if !ok {
		t.Fatal("/quit missing")
	}
	exit, ok := lookupNativeCommand("exit")
	if !ok {
		t.Fatal("/exit missing")
	}
	m := New(Config{})
	if got := exit.Run(m, ""); got != ActionQuit || !m.quit {
		t.Fatalf("/exit returned %v, quit=%v, want the /quit behaviour", got, m.quit)
	}
	if strings.TrimSpace(quit.Name) != "quit" {
		t.Fatalf("quit registration name = %q", quit.Name)
	}
}
