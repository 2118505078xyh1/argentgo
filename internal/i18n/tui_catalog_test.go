package i18n

import (
	"strings"
	"testing"
)

// tuiCatalogKeys 枚举从原 internal/tui/locale.go 迁入的全部键，加上
// 斜杠命令描述（tui.cmd_*_desc）。保持此列表与目录同步，可防止
// 后续新增 TUI 文案时只改一侧。
var tuiCatalogKeys = []string{
	"allow",
	"allow_session",
	"approval_title",
	"approved",
	"approved_session",
	"attempt_cancelled",
	"attempt_failed",
	"attempt_retried",
	"attempt_stream_error",
	"busy",
	"cached",
	"cancelled",
	"cancelling",
	"cmd_clear_desc",
	"cmd_compact_desc",
	"cmd_context_desc",
	"cmd_expand_desc",
	"cmd_exit_desc",
	"cmd_help_desc",
	"cmd_hotkeys_desc",
	"cmd_models_desc",
	"cmd_name_desc",
	"cmd_quit_desc",
	"cmd_resume_desc",
	"cmd_retry_desc",
	"cmd_review_desc",
	"cmd_rename_desc",
	"cmd_session_desc",
	"cmd_sessions_desc",
	"cmd_thinking_desc",
	"cmd_undo_desc",
	"collapsed",
	"color_degraded",
	"commands_off",
	"commands_on",
	"commands_title",
	"compact_done",
	"compact_unwired",
	"copied",
	"copy_failed",
	"danger",
	"default_deny",
	"denied",
	"deny",
	"error",
	"expanded",
	"help",
	"hint",
	"hint_modal",
	"models_info",
	"name_usage",
	"named_off",
	"named_on",
	"no_commands",
	"no_plan",
	"no_sessions",
	"nothing_to_copy",
	"no_tasks",
	"plan_off",
	"plan_on",
	"plan_title",
	"ready",
	"rename_done",
	"rename_unwired",
	"retry_done",
	"retry_unwired",
	"session_delete_done",
	"session_delete_unwired",
	"session_open_done",
	"session_open_unwired",
	"session_resume_prompt",
	"session_rename_prompt",
	"session_rename_unwired",
	"sessions_off",
	"sessions_on",
	"sessions_title",
	"sort_toggled",
	"tasks_off",
	"tasks_on",
	"tasks_title",
	"thinking_info",
	"theme",
	"thinking",
	"thinking_all_off",
	"thinking_all_on",
	"tool",
	"undo_done",
	"undo_unwired",
	"working",
	"workspace_off",
	"workspace_on",
	"workspace_title",
}

// TestTuiCatalogKeysComplete 检查迁移列表与目录里的 tui. 键集合完全一致，
// 且每条翻译的 en/zh 文本框都非空。
func TestTuiCatalogKeysComplete(t *testing.T) {
	ResetMissing()
	defer ResetMissing()

	want := make(map[string]bool, len(tuiCatalogKeys))
	for _, key := range tuiCatalogKeys {
		full := "tui." + key
		if want[full] {
			t.Errorf("tuiCatalogKeys 重复键: %q", key)
		}
		want[full] = true

		entry, ok := catalog[full]
		if !ok {
			t.Errorf("catalog 缺少 TUI 键 %q", full)
			continue
		}
		if entry.en == "" {
			t.Errorf("catalog 键 %q 的 en 文本为空", full)
		}
		if entry.zh == "" {
			t.Errorf("catalog 键 %q 的 zh 文本为空", full)
		}
	}

	for _, full := range Keys() {
		if strings.HasPrefix(full, "tui.") && !want[full] {
			t.Errorf("catalog 存在未登记的 TUI 键 %q", full)
		}
	}

	if missing := Missing(); len(missing) != 0 {
		t.Errorf("直接读取 catalog 不应记录缺失键: %v", missing)
	}
}

// TestTuiCatalogTRespectsLocale 验证 T 对两种语言及常见别名返回正确的文本框。
func TestTuiCatalogTRespectsLocale(t *testing.T) {
	ResetMissing()
	defer ResetMissing()

	for _, key := range tuiCatalogKeys {
		full := "tui." + key
		entry, ok := catalog[full]
		if !ok {
			t.Errorf("catalog 缺少 TUI 键 %q", full)
			continue
		}
		if got := T(ZhCN, full); got != entry.zh {
			t.Errorf("T(%q, %q) = %q, want zh %q", ZhCN, full, got, entry.zh)
		}
		if got := T(EnUS, full); got != entry.en {
			t.Errorf("T(%q, %q) = %q, want en %q", EnUS, full, got, entry.en)
		}
		if got := T("zh", full); got != entry.zh {
			t.Errorf("T(%q, %q) = %q, want zh %q", "zh", full, got, entry.zh)
		}
		if got := T("en", full); got != entry.en {
			t.Errorf("T(%q, %q) = %q, want en %q", "en", full, got, entry.en)
		}
	}

	if missing := Missing(); len(missing) != 0 {
		t.Errorf("已迁移的 TUI 键不应触发缺失记录: %v", missing)
	}
}

// TestTuiCatalogLiteralSamples 固化几个高风险字面量：省略号、CJK 标点、
// 方向箭头与超长 help 文本，防止迁移时被"顺手修正"导致字节漂移。
func TestTuiCatalogLiteralSamples(t *testing.T) {
	cases := []struct {
		key string
		en  string
		zh  string
	}{
		{"hint", "enter send · alt+enter newline · pgup/pgdn scroll · ctrl+c cancel · /help", "enter 发送 · alt+enter 换行 · pgup/pgdn 滚动 · ctrl+c 取消 · /help"},
		{"hint_modal", "y allow · n deny · a allow for session · esc deny", "y 允许 · n 拒绝 · a 本次会话允许 · esc 拒绝"},
		{"working", "working…", "思考中…"},
		{"cancelling", "cancelling…", "正在取消…"},
		{"name_usage", "usage: /name <title>", "用法：/name <标题>"},
		{"workspace_on", "workspace panel shown (↑↓ move, ctrl+b to hide)", "已显示工作区面板（↑↓ 移动，ctrl+b 隐藏）"},
		{"help", "enter sends · alt+enter/ctrl+j newline · ctrl+b/ctrl+f cursor · ctrl+a/ctrl+e line · ctrl+d exit on empty · ctrl+c cancels, again quits · ctrl+t thinking · ctrl+o tool output · ctrl+x copy · ctrl+g editor · ctrl+l/ctrl+p model · alt+t tasks · alt+b workspace · alt+p plan · alt+s sessions · alt+c commands · alt+u undo · alt+r retry · /clear /expand /undo /retry /name <title> /quit /help /sessions /models /thinking /compact /hotkeys (markdown commands expand $ARGUMENTS / $1..$N)", "enter 发送 · alt+enter/ctrl+j 换行 · ctrl+b/ctrl+f 移动 · ctrl+a/ctrl+e 行首尾 · ctrl+d 空行退出 · ctrl+c 取消，再按退出 · ctrl+t 思考块 · ctrl+o 工具输出 · ctrl+x 复制 · ctrl+g 外编辑器 · ctrl+l/ctrl+p 模型 · alt+t 任务 · alt+b 工作区 · alt+p 计划 · alt+s 会话 · alt+c 命令 · alt+u 撤销 · alt+r 重试 · /clear /expand /undo /retry /name <标题> /quit /help /sessions /models /thinking /compact /hotkeys（markdown 命令支持 $ARGUMENTS / $1..$N）"},
	}
	for _, tc := range cases {
		if got := T(EnUS, "tui."+tc.key); got != tc.en {
			t.Errorf("T(EnUS, %q) 字节不一致\n got: %q\nwant: %q", tc.key, got, tc.en)
		}
		if got := T(ZhCN, "tui."+tc.key); got != tc.zh {
			t.Errorf("T(ZhCN, %q) 字节不一致\n got: %q\nwant: %q", tc.key, got, tc.zh)
		}
	}
}
