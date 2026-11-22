package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/config"
)

// 本文件实现顶层子命令 `argent opt-ins`：给出所有已知 opt-in 的统一清单。
// R38 研究发现 14 个集中式键（ARGENT_OPT_INS）与大量散落的独立门控缺少
// 一个可发现的入口；这里把它们汇总为一份稳定的、只读的报告。
//
// 复用已有原语而不改动它们：
//   - config.ParseOptInDisplay 给出 14 个集中式键的标签与归类；
//   - IndependentOptIns 是独立门控的单一真源（optin_metadata.go）；
//   - OptInFromEnv / FormatOptIns 从注入的 env 读取并格式化注册表状态。
// 本命令不读取进程环境（一律经 App.IO.Env），不写文件，不改变任何运行行为。

// optInEntry 是一个 opt-in 开关的展示投影。所有字段都带稳定 JSON 标签，
// 便于脚本消费；Category 为空时 JSON 中省略。
type optInEntry struct {
	Key         string `json:"key"`
	Env         string `json:"env"`
	Value       string `json:"value"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description"`
	Category    string `json:"category,omitempty"`
}

// centralizedOptInPurpose 给出 15 个集中式键的简短用途，补齐标签之外的说明。
// 键集合以 config.ParseOptInDisplay 为准；这里只提供文案，不参与白名单判定。
var centralizedOptInPurpose = map[string]string{
	"apply_patch":               "启用结构化补丁工具行为",
	"auto_verify":               "编辑后自动派生已有的验证诊断回环",
	"cli_hints":                 "输出中附带 CLI 提示",
	"compaction_fidelity":       "压缩摘要要素缺失时发出质量告警",
	"context_pressure":          "暴露上下文压力信号",
	"grep_ex":                   "启用扩展 grep 搜索行为",
	"keep_implicit_context":     "把压缩丢弃的隐式上下文并入摘要",
	"model_summary":             "压缩时用模型生成增量叙事摘要",
	"preserve_summary_sections": "重建摘要时保留原摘要的关键章节",
	"refusal_reason":            "在结果中暴露模型的拒绝原因",
	"tool_error_reflection":     "工具失败时向模型回灌结构化错误反射",
	"attempt_isolation":         "验证失败时把失败尝试分叉为旁支会话",
	"untrusted_boundary":        "启用不可信内容边界标记",
	"untrusted_result":          "把工具结果标记为不可信数据，降低提示注入风险",
	"codegraph":                 "构建本地代码索引并提供符号定义与引用查询工具",
}

// runOptIns 实现 `argent opt-ins [--json]`。退出码约定与 doctor 一致：
// -h/--help 打印用法返回 0；未知 flag 或位置参数返回 2；输出失败返回 1。
func runOptIns(app App, _ context.Context, args []string) int {
	asJSON, help, err := optInsFlags(args)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "argentgo: opt-ins: %v\n", err)
		optInsUsage(app.IO.Err)
		return 2
	}
	if help {
		optInsUsage(app.IO.Out)
		return 0
	}

	entries := optInEntries(app.env)
	if asJSON {
		data, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}
	writeOptInsText(app.IO.Out, entries, app.env)
	return 0
}

// optInsFlags 解析本子命令的极小 flag 集合。它不调用 parseArgs，
// 因为 --json 是子命令私有的 flag，不属于全局 flag 表面（也不进 parity）。
func optInsFlags(args []string) (asJSON, help bool, err error) {
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			return false, true, nil
		case arg == "--json":
			asJSON = true
		case strings.HasPrefix(arg, "-"):
			return false, false, fmt.Errorf("unknown flag %q", arg)
		default:
			return false, false, fmt.Errorf("unexpected argument %q", arg)
		}
	}
	return asJSON, false, nil
}

// optInEntries 汇总注册表键与独立门控，按 Key 升序稳定排序。
// 当前值一律从注入的 env 读取：集中式键显示 ARGENT_OPT_INS 原始串，
// 独立门控显示各自环境变量的原始串；未设置时为空串（文本输出显示 (unset)）。
func optInEntries(env func(string) string) []optInEntry {
	enabled := OptInFromEnv(env)
	rawList := ""
	if env != nil {
		rawList = env(optInEnvVar)
	}

	entries := make([]optInEntry, 0, len(centralizedOptInPurpose)+len(IndependentOptIns))
	for _, item := range config.ParseOptInDisplay(nil) {
		description := item.Label
		if purpose, ok := centralizedOptInPurpose[item.Key]; ok && purpose != "" {
			description = item.Label + "：" + purpose
		}
		entries = append(entries, optInEntry{
			Key:         item.Key,
			Env:         optInEnvVar,
			Value:       rawList,
			Enabled:     enabled[item.Key],
			Description: description,
			Category:    item.Category,
		})
	}
	for _, item := range IndependentOptIns {
		raw := ""
		if env != nil {
			raw = env(item.Env)
		}
		entries = append(entries, optInEntry{
			Key:         item.Key,
			Env:         item.Env,
			Value:       raw,
			Enabled:     independentOptInEnabled(item, raw, env),
			Description: item.Name + "：" + item.Desc,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Key != entries[j].Key {
			return entries[i].Key < entries[j].Key
		}
		return entries[i].Env < entries[j].Env
	})
	return entries
}

// independentOptInEnabled 依据登记表的 Enable 摘要判定门控是否开启。
// 它是一份展示用的最佳努力解释，与各读点的精确语义同源（Enable 字段），
// 不替代任何运行时门控；历史占位（未接线）恒为关闭。
func independentOptInEnabled(item IndependentOptIn, raw string, env func(string) string) bool {
	if item.Historical {
		return false
	}
	value := strings.TrimSpace(raw)
	switch {
	case item.OptInKey != "" && strings.Contains(item.Enable, "ARGENT_OPT_INS="):
		return value == "1" || optInListHas(env, item.OptInKey)
	case strings.Contains(item.Enable, "真值"):
		return envTruthy(value)
	case strings.Contains(item.Enable, "0 或空关闭"):
		return value != "" && value != "0"
	case strings.Contains(item.Enable, "正整数"):
		n, err := strconv.Atoi(value)
		return err == nil && n > 0
	case strings.Contains(item.Enable, "enforce"), strings.Contains(item.Enable, "非空"):
		return value != ""
	case strings.Contains(item.Enable, "true/yes/on"):
		switch strings.ToLower(value) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	default:
		return value == "1"
	}
}

// envTruthy 收敛真值写法：1/t/true/y/yes/on（大小写不敏感、忽略首尾空白）。
// 先走 strconv.ParseBool 与源码里的解析保持一致，再额外接受 yes/on/y。
func envTruthy(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if on, err := strconv.ParseBool(trimmed); err == nil {
		return on
	}
	switch strings.ToLower(trimmed) {
	case "y", "yes", "on":
		return true
	default:
		return false
	}
}

// optInListHas 检查 ARGENT_OPT_INS 原始串是否含某个键。它不经过
// OptInFromEnv 的白名单，因此也能识别 jobs 这类注册表外的同义键。
func optInListHas(env func(string) string, key string) bool {
	if env == nil {
		return false
	}
	for _, field := range strings.Split(env(optInEnvVar), ",") {
		if strings.TrimSpace(field) == key {
			return true
		}
	}
	return false
}

// writeOptInsText 输出人类可读的稳定表格。列宽按 rune 计数（fmt 的 %-Ns
// 语义），因此中文说明不会在 UTF-8 字符中间被切开。
func writeOptInsText(w io.Writer, entries []optInEntry, env func(string) string) {
	enabledCount := 0
	for _, entry := range entries {
		if entry.Enabled {
			enabledCount++
		}
	}
	fmt.Fprintf(w, "已知 opt-in 开关（共 %d 项，当前启用 %d 项）\n", len(entries), enabledCount)
	fmt.Fprintf(w, "ARGENT_OPT_INS 注册表：%s\n\n", FormatOptIns(OptInFromEnv(env)))
	fmt.Fprintf(w, "%-32s %-32s %-26s %-6s %s\n", "键名", "环境变量", "当前值", "状态", "说明")
	for _, entry := range entries {
		state := "关闭"
		if entry.Enabled {
			state = "开启"
		}
		value := entry.Value
		if value == "" {
			value = "(unset)"
		}
		fmt.Fprintf(w, "%-32s %-32s %-26s %-6s %s\n", entry.Key, entry.Env, value, state, entry.Description)
	}
}

func optInsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo opt-ins [--json]

List every known opt-in switch: the 14-key ARGENT_OPT_INS registry plus the
standalone ARGENT_* gates. For each entry print its key, environment variable,
current raw env value (from the injected env) and a short Chinese description.
With --json, emit a stable JSON array instead of the text table.
`)
}
