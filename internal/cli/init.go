package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/auth"
	"github.com/2118505078xyh1/argentgo/internal/provider"
)

// 本文件的用户可见文案全部走简体中文常量，与 maybeFirstRunGuide 的既有惯例
// 保持一致。刻意不新增"含空格的英文硬编码"：internal/i18n 的增量守卫只统计
// 这类字面量，中文不会被计入，因此英文基线不增长。若日后统一迁入 i18n 目录，
// 可按下面的分组逐条替换，不必改动交互逻辑。
const (
	initWizardTitle = " Argentgo 初始化向导（随时可 Ctrl+C 退出，之后运行 argentgo init 可继续）"

	initStepProvider   = "[1/4] 选择模型提供方"
	initStepCredential = "[2/4] 配置凭据"
	initStepConfig     = "[3/4] 生成配置"
	initStepDoctor     = "[4/4] 自检"

	initProviderHint      = "  本地服务免 API key，优先推荐；云端凭据可稍后再配。"
	initDetectedMark      = "  ← 已探测到服务，推荐"
	initSkipLabel         = "跳过，稍后再配置"
	initSkipDone          = "已跳过初始化；稍后可运行 argentgo init。"
	initEOFDone           = "输入已结束，已退出初始化；稍后可运行 argentgo init。"
	initLocalNoCredential = "是本地提供方，无需 API key；已按内置模板配置 base URL 与模型。"

	initCredentialHint       = "  凭据用于向云端提供方证明身份；可随时延后，稍后用 argentgo login 配置。"
	initCredentialDevice     = "设备码登录（授权后自动保存，推荐）"
	initCredentialAPIKey     = "输入 API key"
	initCredentialLater      = "稍后再说"
	initCredentialLaterMsg   = "  已选择稍后配置凭据。"
	initCredentialAPIKeyAsk  = "  请输入 %s 的 API key（不会回显；直接回车跳过）: "
	initCredentialEmptyKey   = "  未输入 key，稍后可配置。"
	initCredentialDeviceFail = "  设备码登录未完成；配置仍会生成，稍后可重试。"

	initConfigPathLine     = "  将写入 %s（带注释的最小配置，JSONC）。"
	initConfigPreserveLine = "  现有配置不会被覆盖，只补充缺失项。"
	initConfigWillAdd      = "  将补充以下配置："
	initConfigHidden       = "<已隐藏>"
	initConfigConfirm      = "  确认 [Y/n]: "
	initConfigSkip         = "  已跳过配置写入。"
	initConfigNoChange     = "  现有配置已包含必要项，未做改动。"
	initConfigWritten      = "  已写入 %s。"
	initConfigReadFail     = "  读取现有配置失败："
	initConfigWriteFail    = "  写入配置失败："
	initConfigCommentHead  = "// 本文件由 argentgo init 生成或补全，仅包含必要项。\n" +
		"// 配置支持注释与尾随逗号（JSONC）；可用 argentgo config show 查看生效值。\n"

	initDoctorRunning  = "  正在运行 argentgo doctor ..."
	initDoctorCredWarn = "  凭据未就绪：可运行 argentgo login %s --api-key <key>%s"
	initDoctorOK       = "  ✓ 自检通过。"
	initDoctorFail     = "  ⚠ 自检发现失败项，请按上方 doctor 输出处理；稍后可重新运行 argentgo doctor。"
	initDoctorDone     = "  完成。现在可以：argentgo tui  或  argentgo \"帮我写个函数\""
	initNoTTY          = "argentgo: init 需要交互终端，请在终端中运行；非交互场景请改用 argentgo login 或 argentgo config set。"
	initBadArg         = "argentgo: init: 未知参数 %q"
	initBadChoice      = "  输入无效，请输入 1-%d（直接回车选择 %d）。"
)

// initInteractive 要求 stdout 连接交互终端；若注入了 stdin 字符设备检查，
// 还要求 stdin 本身是终端（管道/重定向明确拒绝，且不读取、不挂起）。
func initInteractive(app App) bool {
	if app.IO.HasTerminal == nil || !app.IO.HasTerminal() {
		return false
	}
	if app.IO.StdinIsCharDevice != nil && !app.IO.StdinIsCharDevice() {
		return false
	}
	return true
}

// 以下接缝与 deviceStart/devicePoll 同属"可注入入口"，测试替换即可覆盖
// 端口探测、doctor 自检与设备码登录，无需真实网络或控制台。
var (
	initProbe = func(addr string, timeout time.Duration) error {
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			return err
		}
		return conn.Close()
	}
	initDoctor      = runDoctor
	initDeviceLogin = runDeviceLogin
)

const initProbeTimeout = 300 * time.Millisecond

// initProviderChoice 是第一步展示的一个候选项。
type initProviderChoice struct {
	id       string
	label    string
	template provider.Template
	detected bool
	cloud    bool
	skip     bool
}

// initProviderChoices 构造候选列表：本地免 key 项在前，探测到服务的项预选；
// 云端默认取 ARGENT_PROVIDER（无则 openai），最后一项固定为"跳过"。
func initProviderChoices(app App) []initProviderChoice {
	choices := make([]initProviderChoice, 0, 4)
	for _, item := range []struct{ name, addr string }{
		{"ollama", "127.0.0.1:11434"},
		{"lmstudio", "127.0.0.1:1234"},
	} {
		tmpl, ok := provider.Resolve(item.name)
		if !ok {
			continue
		}
		label := fmt.Sprintf("%s（本地，免 API key）", tmpl.Name)
		choices = append(choices, initProviderChoice{
			id:       tmpl.Name,
			label:    label,
			template: tmpl,
			detected: initProbe(item.addr, initProbeTimeout) == nil,
		})
	}
	cloudName := firstNonEmpty(app.env("ARGENT_PROVIDER"), "openai")
	tmpl, ok := provider.Resolve(cloudName)
	if !ok {
		tmpl, _ = provider.Resolve("openai")
		cloudName = tmpl.Name
	}
	choices = append(choices, initProviderChoice{
		id:       cloudName,
		label:    fmt.Sprintf("%s（云端，需登录或 API key）", tmpl.Name),
		template: tmpl,
		cloud:    true,
	})
	choices = append(choices, initProviderChoice{id: "skip", label: initSkipLabel, skip: true})
	return choices
}

// initReadLine 读取一行，EOF 且无内容时返回 false，避免管道输入造成死循环。
func initReadLine(r *bufio.Reader) (string, bool) {
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false
	}
	if errors.Is(err, io.EOF) && line == "" {
		return "", false
	}
	return strings.TrimRight(line, "\r\n"), true
}

// initAskInt 读取 1..max 的选择；空行取默认值，最多重试三次，EOF 视为放弃。
func initAskInt(r *bufio.Reader, out io.Writer, def, max int) (int, bool) {
	for attempt := 0; attempt < 3; attempt++ {
		line, ok := initReadLine(r)
		if !ok {
			return 0, false
		}
		text := strings.TrimSpace(line)
		if text == "" {
			return def, true
		}
		n, err := strconv.Atoi(text)
		if err == nil && n >= 1 && n <= max {
			return n, true
		}
		fmt.Fprintf(out, initBadChoice, max, def)
	}
	return 0, false
}

// initConfirm 读取 Y/n 确认；空行视为 Y，EOF 视为放弃。
func initConfirm(r *bufio.Reader, out io.Writer) (bool, bool) {
	line, ok := initReadLine(r)
	if !ok {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true, true
	default:
		return false, true
	}
}

// initCredentialStep 执行第二步：本地提供方无需凭据；云端按可用性提供
// 设备码登录 / API key / 稍后再说，且默认落在"稍后再说"以便随时延后。
func initCredentialStep(app App, ctx context.Context, r *bufio.Reader, c initProviderChoice) (string, int) {
	fmt.Fprintln(app.IO.Out, initStepCredential)
	if !c.cloud {
		fmt.Fprintf(app.IO.Out, "  %s %s\n", c.template.Name, initLocalNoCredential)
		return "", 0
	}

	preset, ok := auth.Preset(c.template.Name)
	deviceOK := ok && preset.Kind == auth.KindDevice
	options := []string{initCredentialAPIKey, initCredentialLater}
	if deviceOK {
		options = []string{initCredentialDevice, initCredentialAPIKey, initCredentialLater}
	}
	fmt.Fprintln(app.IO.Out, initCredentialHint)
	for i, label := range options {
		fmt.Fprintf(app.IO.Out, "  %d) %s\n", i+1, label)
	}
	def := len(options) // 默认延后，不替用户做登录决定
	fmt.Fprintf(app.IO.Out, "  选择 [%d]: ", def)
	choice, ok := initAskInt(r, app.IO.Out, def, len(options))
	if !ok || choice == len(options) {
		fmt.Fprintln(app.IO.Out, initCredentialLaterMsg)
		return "", 0
	}
	if deviceOK && choice == 1 {
		if code := initDeviceLogin(app, ctx, c.template.Name); code != 0 {
			fmt.Fprintln(app.IO.Out, initCredentialDeviceFail)
		}
		return "", 0
	}
	fmt.Fprintf(app.IO.Out, initCredentialAPIKeyAsk, c.template.Name)
	line, ok := initReadLine(r)
	if !ok {
		return "", 0
	}
	key := strings.TrimSpace(line)
	if key == "" {
		fmt.Fprintln(app.IO.Out, initCredentialEmptyKey)
	}
	return key, 0
}

// initAddition 是一条待补写的最小配置项，path 支持点分嵌套。
type initAddition struct {
	path    string
	value   any
	comment string
}

// initPlanAdditions 给出选定提供方的最小配置：provider/model/baseURL，
// 协议与 OpenAI 默认不一致时才补 protocol，拿到 key 时再补 providers.<name>.apiKey。
func initPlanAdditions(c initProviderChoice, apiKey string) []initAddition {
	plan := []initAddition{
		{path: "provider", value: c.template.Name, comment: "模型提供方模板 id"},
	}
	if c.template.Protocol != provider.ProtocolOpenAI {
		plan = append(plan, initAddition{path: "protocol", value: string(c.template.Protocol), comment: "线上协议"})
	}
	plan = append(plan,
		initAddition{path: "model", value: c.template.DefaultModel, comment: "默认使用的模型"},
		initAddition{path: "baseURL", value: c.template.BaseURL, comment: "API 根地址（本地服务为回环地址）"},
	)
	if apiKey != "" {
		plan = append(plan, initAddition{
			path:    "providers." + c.template.Name + ".apiKey",
			value:   apiKey,
			comment: "API key（请勿提交到仓库）",
		})
	}
	return plan
}

// initAliases 返回与 path 等价的键名，兼容配置解码器接受的别名，
// 避免已有 baseUrl/providers.x.api_key 时被重复写入规范键。
func initAliases(path string) []string {
	switch path {
	case "model":
		return []string{"model", "modelName"}
	case "baseURL":
		return []string{"baseURL", "baseUrl", "base_url"}
	}
	if strings.HasSuffix(path, ".apiKey") {
		base := strings.TrimSuffix(path, ".apiKey")
		return []string{path, base + ".api_key", base + ".api-key"}
	}
	return []string{path}
}

func initMissing(cfg map[string]any, path string) bool {
	value, ok := getDotted(cfg, path)
	if !ok {
		return true
	}
	text, isString := value.(string)
	return isString && strings.TrimSpace(text) == ""
}

func initMissingAny(cfg map[string]any, paths ...string) bool {
	for _, path := range paths {
		if !initMissing(cfg, path) {
			return false
		}
	}
	return true
}

func initCloneMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = initCloneValue(value)
	}
	return out
}

func initCloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return initCloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = initCloneValue(item)
		}
		return out
	default:
		return value
	}
}

// initApplyAdditions 只补缺失项：现有键（含别名）非空时原样保留，
// 返回合并后的配置与实际补写的条目。
func initApplyAdditions(existing map[string]any, plan []initAddition) (map[string]any, []initAddition) {
	merged := initCloneMap(existing)
	var applied []initAddition
	for _, item := range plan {
		if !initMissingAny(merged, initAliases(item.path)...) {
			continue
		}
		setDotted(merged, item.path, item.value)
		applied = append(applied, item)
	}
	return merged, applied
}

// renderInitJSONC 生成带注释的 JSONC：键按字典序稳定排列，
// 注释挂在补写项的完整点分路径上。config 的加载器会剥离注释。
func renderInitJSONC(cfg map[string]any, comments map[string]string) []byte {
	var b strings.Builder
	b.WriteString(initConfigCommentHead)
	initWriteJSONCValue(&b, cfg, 0, "", comments)
	b.WriteByte('\n')
	return []byte(b.String())
}

func initWriteJSONCValue(b *strings.Builder, value any, depth int, path string, comments map[string]string) {
	indent := strings.Repeat("  ", depth)
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		b.WriteString("{\n")
		for i, key := range keys {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			b.WriteString(indent)
			b.WriteString("  ")
			b.WriteString(strconv.Quote(key))
			b.WriteString(": ")
			initWriteJSONCValue(b, typed[key], depth+1, childPath, comments)
			// 逗号必须写在注释之前：StripJSONC 会吃掉 `//` 到行尾的全部字节，
			// 若逗号落在注释之后会被一并删除，导致 JSON 缺分隔符。
			if i < len(keys)-1 {
				b.WriteByte(',')
			}
			if comment, ok := comments[childPath]; ok {
				b.WriteString(" // ")
				b.WriteString(comment)
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent)
		b.WriteByte('}')
	default:
		data, err := json.Marshal(value)
		if err != nil {
			b.WriteString("null")
			return
		}
		b.Write(data)
	}
}

// initConfigStep 执行第三步：预览补写内容、征求确认，确认后原子写入 JSONC。
// 返回 wrote 表示是否真的写了文件——用户跳过或无变化时不进入自检。
func initConfigStep(app App, r *bufio.Reader, c initProviderChoice, apiKey string) (bool, int) {
	fmt.Fprintln(app.IO.Out, initStepConfig)
	path := app.configPath()
	fmt.Fprintf(app.IO.Out, initConfigPathLine, path)
	fmt.Fprintln(app.IO.Out, initConfigPreserveLine)

	existing, err := loadConfigFile(path)
	if err != nil {
		fmt.Fprintln(app.IO.Err, initConfigReadFail, err)
		return false, 1
	}
	merged, applied := initApplyAdditions(existing, initPlanAdditions(c, apiKey))
	if len(applied) == 0 {
		fmt.Fprintln(app.IO.Out, initConfigNoChange)
		return true, 0
	}

	fmt.Fprintln(app.IO.Out, initConfigWillAdd)
	for _, item := range applied {
		shown := item.value
		if strings.HasSuffix(item.path, ".apiKey") {
			shown = initConfigHidden
		}
		fmt.Fprintf(app.IO.Out, "    %s = %v\n", item.path, shown)
	}
	fmt.Fprintf(app.IO.Out, initConfigConfirm)
	yes, ok := initConfirm(r, app.IO.Out)
	if !ok || !yes {
		fmt.Fprintln(app.IO.Out, initConfigSkip)
		return false, 0
	}

	comments := make(map[string]string, len(applied))
	for _, item := range applied {
		comments[item.path] = item.comment
	}
	if err := writeConfigBytes(path, renderInitJSONC(merged, comments)); err != nil {
		fmt.Fprintln(app.IO.Err, initConfigWriteFail, err)
		return false, 1
	}
	fmt.Fprintf(app.IO.Out, initConfigWritten, path)
	return true, 0
}

// initDeviceHint 仅在提供方确实支持设备码流程时给出对应命令。
func initDeviceHint(providerName string) string {
	if preset, ok := auth.Preset(providerName); ok && preset.Kind == auth.KindDevice {
		return "，或 argentgo login " + providerName + " --device"
	}
	return ""
}

// initDoctorStep 执行第四步：先用三条本地检查给出可读摘要，再运行完整 doctor，
// 最后按退出码解释结果并给出下一步。向导本身始终返回 0——自检失败不拦路。
func initDoctorStep(app App, ctx context.Context, c initProviderChoice) int {
	fmt.Fprintln(app.IO.Out, initStepDoctor)
	fmt.Fprintln(app.IO.Out, initDoctorRunning)

	checks := []doctorCheck{checkConfigFile(app), checkStorage(app), checkCredential(app)}
	parts := make([]string, 0, len(checks))
	for _, check := range checks {
		mark := "✓"
		if check.status != statusOK {
			mark = "⚠"
		}
		parts = append(parts, mark+" "+check.name)
	}
	fmt.Fprintln(app.IO.Out, "  "+strings.Join(parts, "  "))
	if cred := checks[len(checks)-1]; cred.status != statusOK {
		fmt.Fprintf(app.IO.Out, initDoctorCredWarn, c.template.Name, initDeviceHint(c.template.Name))
	}

	if code := initDoctor(app, ctx, []string{"--base-url", c.template.BaseURL}); code == 0 {
		fmt.Fprintln(app.IO.Out, initDoctorOK)
	} else {
		fmt.Fprintln(app.IO.Out, initDoctorFail)
	}
	fmt.Fprintln(app.IO.Out, initDoctorDone)
	return 0
}

// runInit 实现 `argentgo init`：四步交互向导。任何一步都可跳过；
// 不写 onboarded 标记，不覆盖现有配置，非 TTY 明确报错并非零退出。
func runInit(app App, ctx context.Context, args []string) int {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			initUsage(app.IO.Out)
			return 0
		}
		fmt.Fprintf(app.IO.Err, initBadArg, arg)
		initUsage(app.IO.Err)
		return 2
	}
	if !initInteractive(app) {
		fmt.Fprintln(app.IO.Err, initNoTTY)
		return 2
	}

	fmt.Fprintln(app.IO.Out, initWizardTitle)
	fmt.Fprintln(app.IO.Out)
	r := bufio.NewReader(app.IO.In)
	choices := initProviderChoices(app)

	def := -1
	for i, choice := range choices {
		if choice.detected {
			def = i
			break
		}
	}
	if def < 0 {
		def = len(choices) - 2 // 默认云端，跳过项在最后
	}

	fmt.Fprintln(app.IO.Out, initStepProvider)
	fmt.Fprintln(app.IO.Out, initProviderHint)
	for i, choice := range choices {
		mark := ""
		if choice.detected {
			mark = initDetectedMark
		}
		fmt.Fprintf(app.IO.Out, "  %d) %s%s\n", i+1, choice.label, mark)
	}
	fmt.Fprintf(app.IO.Out, "  选择 [%d]: ", def+1)
	index, ok := initAskInt(r, app.IO.Out, def+1, len(choices))
	if !ok {
		fmt.Fprintln(app.IO.Out, initEOFDone)
		return 0
	}
	choice := choices[index-1]
	if choice.skip {
		fmt.Fprintln(app.IO.Out, initSkipDone)
		return 0
	}

	apiKey, code := initCredentialStep(app, ctx, r, choice)
	if code != 0 {
		return code
	}
	wrote, code := initConfigStep(app, r, choice, apiKey)
	if code != 0 {
		return code
	}
	if !wrote {
		return 0
	}
	return initDoctorStep(app, ctx, choice)
}

func initUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo init

Runs an interactive four-step wizard: choose a provider (local no-key services
are probed and preferred), configure credentials, write a minimal commented
config that only fills in missing keys, then run argentgo doctor. Every step
can be skipped; existing config is never overwritten. Requires a TTY.
`)
}
