package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// runOptInsWith 在注入的 env 下运行 opt-ins 子命令，返回退出码与两路输出。
func runOptInsWith(t *testing.T, args []string, env map[string]string) (int, string, string) {
	t.Helper()
	ioState, out, errOut := newTestIO("", env)
	code := RunWith(App{IO: ioState}, append([]string{"opt-ins"}, args...))
	return code, out.String(), errOut.String()
}

// optInsEntriesFromJSON 把 --json 输出还原为条目列表。
func optInsEntriesFromJSON(t *testing.T, output string) []optInEntry {
	t.Helper()
	var entries []optInEntry
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, output)
	}
	return entries
}

func findOptIn(t *testing.T, entries []optInEntry, key string) optInEntry {
	t.Helper()
	for _, entry := range entries {
		if entry.Key == key {
			return entry
		}
	}
	t.Fatalf("opt-in %q not found in %d entries", key, len(entries))
	return optInEntry{}
}

// findOptInByEnv 在键名可能重复时（cli_hints 同时属于注册表与独立门控）
// 用「键名 + 环境变量」精确定位条目。
func findOptInByEnv(t *testing.T, entries []optInEntry, key, envName string) optInEntry {
	t.Helper()
	for _, entry := range entries {
		if entry.Key == key && entry.Env == envName {
			return entry
		}
	}
	t.Fatalf("opt-in %s/%s not found in %d entries", key, envName, len(entries))
	return optInEntry{}
}

// TestOptInsTextOutput 覆盖默认文本格式：表头、注册表摘要、集中式键与独立门控
// 都出现，且开启/关闭状态与原始 env 值逐字可见。
func TestOptInsTextOutput(t *testing.T) {
	env := map[string]string{
		"ARGENT_OPT_INS":       "cli_hints,apply_patch",
		"ARGENT_TUI_USAGE":     "1",
		"ARGENT_APPROVAL_DIFF": "0",
	}
	code, out, errOut := runOptInsWith(t, nil, env)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, errOut)
	}
	if errOut != "" {
		t.Fatalf("stderr = %q, want empty", errOut)
	}
	for _, want := range []string{
		"已知 opt-in 开关（共 ",
		"当前启用 25 项",
		"ARGENT_OPT_INS 注册表：已启用的 opt-in 特性：",
		"- apply_patch\n- cli_hints\n",
		"键名", "环境变量", "当前值", "状态", "说明",
		"apply_patch", "ARGENT_OPT_INS", "cli_hints,apply_patch",
		"ARGENT_TUI_USAGE", "ARGENT_APPROVAL_DIFF", "(unset)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q\n---\n%s", want, out)
		}
	}
	// 四处开启：两个集中式键 + TUI 用量 + 由 ARGENT_OPT_INS 同义激活的 CLI 提示。
	if got := strings.Count(out, "开启"); got < 4 {
		t.Errorf("stdout has %d occurrences of 开启, want >= 4", got)
	}
	if !strings.Contains(out, "关闭") {
		t.Error("stdout missing closed entries")
	}
}

// TestOptInsJSONOutput 覆盖结构化输出：合法 JSON 数组、字段齐全，
// 且 15 个集中式键与任务点名的独立门控全部在列。
func TestOptInsJSONOutput(t *testing.T) {
	env := map[string]string{
		"ARGENT_OPT_INS":            "refusal_reason",
		"ARGENT_TUI_USAGE":          "1",
		"ARGENT_MCP_IMAGE_ARTIFACT": "true",
	}
	code, out, errOut := runOptInsWith(t, []string{"--json"}, env)
	if code != 0 || errOut != "" {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	entries := optInsEntriesFromJSON(t, out)
	if len(entries) != len(centralizedOptInPurpose)+len(IndependentOptIns) {
		t.Fatalf("entries = %d, want %d", len(entries), len(centralizedOptInPurpose)+len(IndependentOptIns))
	}

	centralized := []string{
		"refusal_reason", "untrusted_result", "context_pressure", "untrusted_boundary",
		"attempt_isolation", "compaction_fidelity", "keep_implicit_context",
		"preserve_summary_sections", "apply_patch", "grep_ex", "cli_hints",
		"tool_error_reflection", "model_summary", "auto_verify", "codegraph",
	}
	for _, key := range centralized {
		entry := findOptInByEnv(t, entries, key, optInEnvVar)
		if entry.Description == "" || entry.Category == "" {
			t.Errorf("centralized entry %+v is incomplete", entry)
		}
	}
	standalone := map[string]string{
		"tui_usage":          "ARGENT_TUI_USAGE",
		"approval_diff":      "ARGENT_APPROVAL_DIFF",
		"retain_guidance":    "ARGENT_RETAIN_GUIDANCE",
		"output_collapse":    "ARGENT_OUTPUT_COLLAPSE",
		"todo_strict":        "ARGENT_TODO_STRICT",
		"summary_schema":     "ARGENT_SUMMARY_SCHEMA",
		"budget_projection":  "ARGENT_BUDGET_PROJECTION",
		"jobs":               "ARGENT_JOBS",
		"bash_use_proc":      "ARGENT_BASH_USE_PROC",
		"path_completion":    "ARGENT_PATH_COMPLETION",
		"vim_mode":           "ARGENT_VIM_MODE",
		"mcp_image_artifact": "ARGENT_MCP_IMAGE_ARTIFACT",
	}
	for key, envName := range standalone {
		entry := findOptIn(t, entries, key)
		if entry.Env != envName || entry.Description == "" {
			t.Errorf("standalone entry %+v is incomplete, want env %s", entry, envName)
		}
	}

	refusal := findOptIn(t, entries, "refusal_reason")
	if !refusal.Enabled || refusal.Value != "refusal_reason" {
		t.Errorf("refusal_reason = %+v, want enabled with raw value", refusal)
	}
	tui := findOptIn(t, entries, "tui_usage")
	if !tui.Enabled || tui.Value != "1" {
		t.Errorf("TUI usage = %+v, want enabled with value 1", tui)
	}
	image := findOptIn(t, entries, "mcp_image_artifact")
	if !image.Enabled || image.Value != "true" {
		t.Errorf("image artifact = %+v, want enabled with value true", image)
	}
	if !strings.Contains(out, `"enabled": true`) || !strings.Contains(out, `"enabled": false`) {
		t.Error("JSON output missing enabled states")
	}
}

// TestOptInsCurrentValueRules 覆盖各类门控的取值规则：严格 =1、真值写法、
// 非零与非空规则，以及 ARGENT_OPT_INS 列表的同义激活路径。
func TestOptInsCurrentValueRules(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		envName string
		env     map[string]string
		on      bool
		value   string
	}{
		{"equals1 padded", "tui_usage", "ARGENT_TUI_USAGE", map[string]string{"ARGENT_TUI_USAGE": " 1 "}, true, " 1 "},
		{"default-on accepts true", "tui_usage", "ARGENT_TUI_USAGE", map[string]string{"ARGENT_TUI_USAGE": "true"}, true, "true"},
		{"0 closes", "tui_usage", "ARGENT_TUI_USAGE", map[string]string{"ARGENT_TUI_USAGE": "0"}, false, "0"},
		{"unset defaults on", "tui_usage", "ARGENT_TUI_USAGE", map[string]string{}, true, ""},
		{"truthy yes", "todo_strict", "ARGENT_TODO_STRICT", map[string]string{"ARGENT_TODO_STRICT": "YES"}, true, "YES"},
		{"truthy false", "todo_strict", "ARGENT_TODO_STRICT", map[string]string{"ARGENT_TODO_STRICT": "false"}, false, "false"},
		{"nonzero", "approval_ttl", "ARGENT_APPROVAL_TTL", map[string]string{"ARGENT_APPROVAL_TTL": "30"}, true, "30"},
		{"nonzero zero", "approval_ttl", "ARGENT_APPROVAL_TTL", map[string]string{"ARGENT_APPROVAL_TTL": "0"}, false, "0"},
		{"nonempty mode", "guardrails", "ARGENT_GUARDRAILS", map[string]string{"ARGENT_GUARDRAILS": "enforce"}, true, "enforce"},
		{"empty mode", "guardrails", "ARGENT_GUARDRAILS", map[string]string{"ARGENT_GUARDRAILS": ""}, false, ""},
		{"cli hints via list", "cli_hints", "ARGENT_CLI_HINTS", map[string]string{"ARGENT_OPT_INS": "cli_hints"}, true, ""},
		{"jobs via list", "jobs", "ARGENT_JOBS", map[string]string{"ARGENT_OPT_INS": "jobs"}, true, ""},
		{"jobs not in registry", "jobs", "ARGENT_JOBS", map[string]string{"ARGENT_OPT_INS": "cli_hints"}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envFn := func(key string) string { return tc.env[key] }
			entry := findOptInByEnv(t, optInEntries(envFn), tc.key, tc.envName)
			if entry.Enabled != tc.on || entry.Value != tc.value {
				t.Fatalf("entry = %+v, want enabled=%v value=%q", entry, tc.on, tc.value)
			}
		})
	}
}

// TestOptInsNoEnvDefaults 覆盖无 env 场景：集中式键仍全部默认关闭，
// 但 10 个「改善体验」独立门控已改为默认开启（=0 才关闭）。
// 文本摘要的注册表部分仍给出固定全关文案，独立门控则如实显示开启。
func TestOptInsNoEnvDefaults(t *testing.T) {
	code, textOut, errOut := runOptInsWith(t, nil, map[string]string{})
	if code != 0 || errOut != "" {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(textOut, "当前启用 24 项") || !strings.Contains(textOut, "全部 opt-in 特性默认关闭") {
		t.Fatalf("text summary = %q", textOut)
	}

	code, jsonOut, errOut := runOptInsWith(t, []string{"--json"}, map[string]string{})
	if code != 0 || errOut != "" {
		t.Fatalf("json exit = %d, stderr = %q", code, errOut)
	}
	entries := optInsEntriesFromJSON(t, jsonOut)
	defaultOnEnvs := map[string]bool{
		"ARGENT_CLI_HINTS": true, "ARGENT_APPROVE_DANGER": true, "ARGENT_APPROVAL_DIFF": true,
		"ARGENT_TUI_USAGE": true, "ARGENT_TUI_PROMPT_HISTORY": true, "ARGENT_PATH_COMPLETION": true,
		"ARGENT_CACHE_VISIBLE": true, "ARGENT_SESSIONS_HEAD_TAIL": true, "ARGENT_APPROVAL_PREVIEW": true,
		"ARGENT_BASH_USE_PROC":   true,
		"ARGENT_GUIDANCE_DEDUPE": true, "ARGENT_SUMMARY_EMPTY_RETRY": true, "ARGENT_ACP_DECODE": true,
		"ARGENT_ELIDE_DATA_URLS": true, "ARGENT_MULTIMODAL_TRIM": true, "ARGENT_TOOL_ARG_FEEDBACK": true,
		"ARGENT_CONFIG_SHADOW_CHECK": true, "ARGENT_CONFIG_RELOAD_DIFF": true, "ARGENT_LOOP_DETECTION": true,
		"ARGENT_MCP_MEDIA_BLOB_EXT": true,
		"ARGENT_VERIFY_OUTCOME":     true, "ARGENT_REPO_MAP_HINT": true, "ARGENT_VERIFY_AUTORUN_HINT": true, "ARGENT_RULE_DISCOVERY": true,
	}
	enabledN := 0
	for _, entry := range entries {
		want := defaultOnEnvs[entry.Env]
		if entry.Enabled != want {
			t.Errorf("%s/%s enabled=%v, want %v: %+v", entry.Key, entry.Env, entry.Enabled, want, entry)
		}
		if entry.Enabled {
			enabledN++
		}
	}
	if enabledN != len(defaultOnEnvs) {
		t.Errorf("enabled count = %d, want %d", enabledN, len(defaultOnEnvs))
	}
	if refusal := findOptIn(t, entries, "refusal_reason"); refusal.Value != "" {
		t.Errorf("raw value = %q, want empty", refusal.Value)
	}
}

// TestOptInsParitySurfaces 直接锁定本命令在 CLI parity 四处接线完整，
// 与 parity_test.go 的全量门禁形成双重保险。
func TestOptInsParitySurfaces(t *testing.T) {
	if _, ok := subcommands["opt-ins"]; !ok {
		t.Error("opt-ins missing from subcommands dispatch map")
	}
	found := false
	for _, name := range subcommandOrder {
		if name == "opt-ins" {
			found = true
			break
		}
	}
	if !found {
		t.Error("opt-ins missing from subcommandOrder")
	}
	found = false
	for _, name := range expectedCommands {
		if name == "opt-ins" {
			found = true
			break
		}
	}
	if !found {
		t.Error("opt-ins missing from expectedCommands")
	}
	var usage strings.Builder
	writeUsage(&usage)
	if !strings.Contains(usage.String(), "opt-ins") {
		t.Error("usage text does not mention opt-ins")
	}
}

// TestOptInsHelpAndBadArgs 覆盖 -h/--help 与非法参数的退出码约定。
func TestOptInsHelpAndBadArgs(t *testing.T) {
	code, out, errOut := runOptInsWith(t, []string{"-h"}, nil)
	if code != 0 || !strings.Contains(out, "usage: argentgo opt-ins [--json]") || errOut != "" {
		t.Fatalf("help: exit = %d, out = %q, err = %q", code, out, errOut)
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--bogus"}, "unknown flag"},
		{[]string{"extra"}, "unexpected argument"},
	} {
		code, out, errOut := runOptInsWith(t, tc.args, nil)
		if code != 2 || out != "" || !strings.Contains(errOut, tc.want) {
			t.Errorf("args %v: exit = %d, out = %q, err = %q", tc.args, code, out, errOut)
		}
	}
}
