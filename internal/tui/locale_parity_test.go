package tui

import (
	"bytes"
	"testing"
)

// preMigrationLocaleStrings 是迁移前 internal/tui/locale.go 双表的逐字副本。
// 它是回归基准：任何标点、省略号、空格或 CJK 字符的漂移都会在这里失败。
var preMigrationLocaleStrings = []struct {
	key string
	en  string
	zh  string
}{
	{"allow", "allow", "允许"},
	{"allow_session", "allow this call only", "仅本次调用允许"},
	{"approval_title", "approval", "需要确认"},
	{"approved", "approved", "已允许"},
	{"approved_session", "approved for this session", "本会话已允许"},
	{"attempt_cancelled", "cancelled", "已取消"},
	{"attempt_failed", "failed", "失败"},
	{"attempt_retried", "retried", "已重试"},
	{"attempt_stream_error", "stream error", "流式错误"},
	{"busy", "a turn is still running", "当前回合尚未结束"},
	{"cached", "cached", "缓存"},
	{"cancelled", "cancelled", "已取消"},
	{"cancelling", "cancelling…", "正在取消…"},
	{"collapsed", "tool output collapsed", "工具输出已折叠"},
	{"color_degraded", "colour output degraded (NO_COLOR or dumb terminal)", "颜色输出已降级（NO_COLOR 或 dumb 终端）"},
	{"commands_off", "commands panel hidden", "已隐藏命令面板"},
	{"commands_on", "commands panel shown (alt+c to hide)", "已显示命令面板（alt+c 隐藏）"},
	{"commands_title", "COMMANDS", "命令"},
	{"danger", "danger", "危险"},
	{"default_deny", "default: deny (enter refuses)", "默认：拒绝（回车即拒绝）"},
	{"denied", "denied", "已拒绝"},
	{"deny", "deny", "拒绝"},
	{"error", "error", "错误"},
	{"expanded", "tool output expanded", "工具输出已展开"},
	{"help", "enter sends · alt+enter/ctrl+j newline · ctrl+b/ctrl+f cursor · ctrl+a/ctrl+e line · ctrl+d exit on empty · ctrl+c cancels, again quits · ctrl+t thinking · ctrl+o tool output · ctrl+x copy · ctrl+g editor · ctrl+l/ctrl+p model · alt+t tasks · alt+b workspace · alt+p plan · alt+s sessions · alt+c commands · alt+u undo · alt+r retry · /clear /expand /undo /retry /name <title> /quit /help /sessions /models /thinking /compact /hotkeys (markdown commands expand $ARGUMENTS / $1..$N)", "enter 发送 · alt+enter/ctrl+j 换行 · ctrl+b/ctrl+f 移动 · ctrl+a/ctrl+e 行首尾 · ctrl+d 空行退出 · ctrl+c 取消，再按退出 · ctrl+t 思考块 · ctrl+o 工具输出 · ctrl+x 复制 · ctrl+g 外编辑器 · ctrl+l/ctrl+p 模型 · alt+t 任务 · alt+b 工作区 · alt+p 计划 · alt+s 会话 · alt+c 命令 · alt+u 撤销 · alt+r 重试 · /clear /expand /undo /retry /name <标题> /quit /help /sessions /models /thinking /compact /hotkeys（markdown 命令支持 $ARGUMENTS / $1..$N）"},
	{"hint", "enter send · alt+enter newline · pgup/pgdn scroll · ctrl+c cancel · /help", "enter 发送 · alt+enter 换行 · pgup/pgdn 滚动 · ctrl+c 取消 · /help"},
	{"hint_modal", "y allow · n deny · a allow this call · esc deny", "y 允许 · n 拒绝 · a 仅本次调用允许 · esc 拒绝"},
	{"name_usage", "usage: /name <title>", "用法：/name <标题>"},
	{"no_commands", "no commands yet", "暂无命令"},
	{"no_plan", "no plan submitted yet (submit_plan)", "尚未提交计划（submit_plan）"},
	{"no_sessions", "no sessions yet", "暂无会话"},
	{"no_tasks", "no tool calls yet", "暂无工具调用"},
	{"plan_off", "plan panel hidden", "已隐藏计划面板"},
	{"plan_on", "plan panel shown (alt+p to hide)", "已显示计划面板（alt+p 隐藏）"},
	{"plan_title", "PLAN", "计划"},
	{"ready", "ready", "就绪"},
	{"rename_done", "session renamed", "会话已重命名"},
	{"rename_unwired", "renaming needs a session hook from the host", "重命名需要宿主提供会话钩子"},
	{"retry_done", "retry requested", "已请求重试"},
	{"retry_unwired", "/retry needs a checkpoint hook from the host", "/retry 需要宿主提供检查点钩子"},
	{"sessions_off", "session rail hidden", "已隐藏会话列表"},
	{"sessions_on", "session rail: enter opens, Ctrl+D deletes, Ctrl+R renames", "会话栏：回车打开，Ctrl+D 删除，Ctrl+R 改名"},
	{"sessions_title", "SESSIONS", "会话"},
	{"tasks_off", "tasks panel hidden", "已隐藏任务面板"},
	{"tasks_on", "tasks panel shown (alt+t to hide)", "已显示任务面板（alt+t 隐藏）"},
	{"tasks_title", "TASKS", "任务"},
	{"theme", "theme", "主题"},
	{"thinking", "thinking", "思考"},
	{"thinking_all_off", "all thinking blocks folded (ctrl+t to expand)", "已折叠全部思考块（ctrl+t 展开）"},
	{"thinking_all_on", "all thinking blocks expanded (ctrl+t to fold)", "已展开全部思考块（ctrl+t 折叠）"},
	{"tool", "tool", "工具"},
	{"undo_done", "undo finished", "撤销完成"},
	{"undo_unwired", "/undo needs a checkpoint hook from the host", "/undo 需要宿主提供检查点钩子"},
	{"working", "working…", "思考中…"},
	{"workspace_off", "workspace panel hidden", "已隐藏工作区面板"},
	{"workspace_on", "workspace panel shown (↑↓ move, alt+b to hide)", "已显示工作区面板（↑↓ 移动，alt+b 隐藏）"},
	{"workspace_title", "WORKSPACE", "工作区"},
}

// TestLocaleParityAgainstPreMigrationStrings 对每个键、每种 locale 逐字节比较
// text() 的返回值与迁移前的硬编码期望值。本地判定沿用旧的 "zh" 前缀规则，
// 因而 "zh-TW" 这类 i18n.Canonical 不认识的变体也必须保持中文输出。
func TestLocaleParityAgainstPreMigrationStrings(t *testing.T) {
	locales := []struct {
		code string
		zh   bool
	}{
		{"en", false},
		{"zh", true},
		{"zh-CN", true},
		{"en-US", false},
		{"", false},
		{"zh-TW", true},
		{"ZH_cn", true},
		{"English", false},
	}
	for _, loc := range locales {
		for _, tc := range preMigrationLocaleStrings {
			want := tc.en
			if loc.zh {
				want = tc.zh
			}
			got := text(loc.code, tc.key)
			if !bytes.Equal([]byte(got), []byte(want)) {
				t.Errorf("locale %q key %q 字节不一致 (got %d bytes, want %d bytes)\n got: %q\nwant: %q",
					loc.code, tc.key, len(got), len(want), got, want)
			}
		}
	}
}

// TestLocaleParityAttemptBadgeKeys 锁定动态构造的 attempt 键：渲染器依赖
// text() 对已迁移键返回本地化文案，而不是回退到原始键名。
func TestLocaleParityAttemptBadgeKeys(t *testing.T) {
	for _, key := range []string{
		"attempt_" + string(AttemptFailed),
		"attempt_" + string(AttemptRetried),
		"attempt_" + string(AttemptCancelled),
		"attempt_" + string(AttemptStreamError),
	} {
		for _, locale := range []string{"en", "zh"} {
			got := text(locale, key)
			if got == key || got == "tui."+key {
				t.Errorf("locale %q 的动态键 %q 未本地化: %q", locale, key, got)
			}
		}
	}
}
