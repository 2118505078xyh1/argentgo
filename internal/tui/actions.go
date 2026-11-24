package tui

import (
	"sort"
	"strings"
)

// ActionID 是一个稳定的、与具体按键解耦的 TUI 动作标识。注册表把
// 规范键串映射到 ActionID，主分派按 ActionID 执行——这是后续允许用户
// 重绑定键位、并由单一事实源生成帮助文案的地基。
//
// 这些 ID 逐项对应 model_input.go 主 switch 的既有语义，不新增行为：
// 默认键位表（DefaultBindings）产生的 ActionID 序列与现有 switch 完全一致。
type ActionID string

const (
	ActNone            ActionID = ""
	ActSubmit          ActionID = "submit"
	ActNewline         ActionID = "newline"
	ActAbort           ActionID = "abort"
	ActQuit            ActionID = "quit"
	ActClearInput      ActionID = "clear_input"
	ActUndo            ActionID = "undo"
	ActRetry           ActionID = "retry"
	ActScrollUp        ActionID = "scroll_up"
	ActScrollDown      ActionID = "scroll_down"
	ActPageUp          ActionID = "page_up"
	ActPageDown        ActionID = "page_down"
	ActToTop           ActionID = "to_top"
	ActToBottom        ActionID = "to_bottom"
	ActToggleTasks     ActionID = "toggle_tasks"
	ActToggleWorkspace ActionID = "toggle_workspace"
	ActTogglePlan      ActionID = "toggle_plan"
	ActToggleSessions  ActionID = "toggle_sessions"
	ActToggleCommands  ActionID = "toggle_commands"
	ActToggleThinking  ActionID = "toggle_thinking"
	ActToggleExpand    ActionID = "toggle_expand"
	ActCopyLast        ActionID = "copy_last"
	ActModelInfo       ActionID = "model_info"
	ActToggleNamedOnly ActionID = "toggle_named_only"
	ActSessionFilter   ActionID = "session_filter"
	ActRenameSession   ActionID = "rename_session"
	ActExternalEditor  ActionID = "external_editor"
	ActKillToEnd       ActionID = "kill_to_end"
	ActKillToStart     ActionID = "kill_to_start"
	ActKillWord        ActionID = "kill_word"
	ActYank            ActionID = "yank"
	ActYankPop         ActionID = "yank_pop"
	ActBackspace       ActionID = "backspace"
	ActDelete          ActionID = "delete"
	ActLeft            ActionID = "left"
	ActRight           ActionID = "right"
	ActLineStart       ActionID = "line_start"
	ActLineEnd         ActionID = "line_end"
	ActHistoryPrev     ActionID = "history_prev"
	ActHistoryNext     ActionID = "history_next"
	ActUp              ActionID = "up"
	ActDown            ActionID = "down"
	ActTab             ActionID = "tab"
	ActEscape          ActionID = "escape"
	ActInsertRune      ActionID = "insert_rune"
	ActPaste           ActionID = "paste"
	ActNavigateUp      ActionID = "navigate_up" // 补全/面板导航中与 up 共用键的语义
	ActNavigateDown    ActionID = "navigate_down"
)

// Bindings 是规范键串 → 动作的映射（一个键只归属一个动作，冲突在构建时裁决）。
type Bindings map[string]ActionID

// DefaultBindings 返回与当前主 switch 完全一致的默认键位表。
// 这是"单一事实源"的种子：未来 help/提示由此生成，用户覆盖在其上叠加。
func DefaultBindings() Bindings {
	return Bindings{
		"enter":     ActSubmit,
		"alt+enter": ActNewline, "ctrl+j": ActNewline,
		"ctrl+c": ActAbort,
		"ctrl+d": ActQuit,
		"ctrl+t": ActToggleThinking,
		"ctrl+o": ActToggleExpand,
		"ctrl+x": ActCopyLast,
		"ctrl+l": ActModelInfo, "ctrl+p": ActModelInfo,
		"ctrl+n":    ActToggleNamedOnly,
		"ctrl+r":    ActRenameSession,
		"ctrl+v":    ActNone,
		"alt+l":     ActSessionFilter,
		"ctrl+home": ActToTop, "ctrl+end": ActToBottom,
		"ctrl+g": ActExternalEditor,
		"alt+u":  ActUndo, "alt+r": ActRetry,
		"ctrl+a": ActLineStart, "ctrl+e": ActLineEnd,
		"ctrl+b": ActLeft, "ctrl+f": ActRight,
		"ctrl+k": ActKillToEnd, "ctrl+u": ActKillToStart, "ctrl+w": ActKillWord,
		"ctrl+y": ActYank, "alt+y": ActYankPop,
		"alt+t": ActToggleTasks, "alt+b": ActToggleWorkspace, "alt+p": ActTogglePlan,
		"alt+s": ActToggleSessions, "alt+c": ActToggleCommands,
		"backspace": ActBackspace, "delete": ActDelete,
		"home": ActLineStart, "end": ActLineEnd,
		"left": ActLeft, "right": ActRight,
		"up": ActUp, "down": ActDown,
		"pgup": ActPageUp, "pgdn": ActPageDown,
		"scroll_up": ActScrollUp, "scroll_down": ActScrollDown,
		"tab": ActTab, "shift+tab": ActTab,
		"esc":   ActEscape,
		"paste": ActPaste,
	}
}

// Resolve 返回规范键串对应的动作。大小写折叠，与 keymap 的 EqualFold 语义一致。
func (b Bindings) Resolve(canonical string) (ActionID, bool) {
	if b == nil {
		return ActNone, false
	}
	want := strings.ToLower(strings.TrimSpace(canonical))
	for key, action := range b {
		if strings.EqualFold(key, want) {
			return action, true
		}
	}
	return ActNone, false
}

// BindingsFromRaw 从配置块解析用户键位覆盖，返回"规范键串 → 动作"表。
// 复用 binding_resolver 的三个配置块名（key_aliases/keybindings/key_bindings），
// 但方向相反：配置里写的是 动作=键串（与 ARGENT_KEY_ALIASES 一致），
// 这里翻转为 键串→ActionID 供分派使用。只接受已知动作，未知动作忽略（fail-closed）。
func BindingsFromRaw(raw map[string]any) Bindings {
	// 直接复用 ParseKeyAliasBlock 把三种块名归一化成 动作→别名列表。
	aliases := ParseKeyAliasBlock(raw)
	out := Bindings{}
	for name, keys := range aliases {
		action := ActionID(strings.ToLower(strings.TrimSpace(name)))
		if action == ActNone {
			continue
		}
		// 只接受在默认表语义中已知的动作（通过 actionDefaultKey 验证）。
		if _, known := actionDefaultKey(action); !known {
			continue
		}
		for _, key := range keys {
			out[strings.ToLower(strings.TrimSpace(key))] = action
		}
	}
	return out
}

// actionDefaultKey 返回某动作在默认表中的规范键串（取字典序最小，确定）。
// 重绑定执行时用它把 ActionID 翻译回默认键，再交给既有主 switch——
// 这样无需重写主分派，任何自定义键位都能复用已验证的执行路径。
func actionDefaultKey(action ActionID) (string, bool) {
	var keys []string
	for key, a := range DefaultBindings() {
		if a == action {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return "", false
	}
	sort.Strings(keys)
	return keys[0], true
}

// Conflict 找出同一键串被声明了多个不同动作的冲突（用户覆盖层用）。
// 返回按键串排序的冲突列表，结果确定。
func Conflict(overrides ...Bindings) []string {
	seen := map[string]ActionID{}
	conflicts := map[string]bool{}
	for _, layer := range overrides {
		for key, action := range layer {
			k := strings.ToLower(strings.TrimSpace(key))
			if prev, ok := seen[k]; ok && prev != action {
				conflicts[k] = true
			} else {
				seen[k] = action
			}
		}
	}
	out := make([]string, 0, len(conflicts))
	for k := range conflicts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
