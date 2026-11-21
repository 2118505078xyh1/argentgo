package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/proc"
)

const (
	// verifyOutputCap 限制捕获的 stdout+stderr 尾部大小。只保留最后 cap 字节，
	// 并在 rune 边界处切断，使 UTF-8 永不被拆分。
	verifyOutputCap   = 32 * 1024
	verifyTruncMarker = "...[output truncated]..."
	verifyLabelMax    = 120

	// verifyShellMeta 列出分隔符、重定向、替换和控制字符。这里并不使用 shell；
	// 拒绝它们属于纵深防御，以免未来的重构意外地让它们生效。
	verifyShellMeta = ";|&<>()$`'\";\n\r\x00^"

	defaultVerifyTimeoutMS = 30000
	maxVerifyTimeoutMS     = 120000

	// verifyAllLanguagesKey 对应 commands.VerifyAllLanguages：针对没有特定语言命令时，
	// 适用于所有已编辑文件的命令的映射键。保持为本地字面量，使 app 无需为一个字面量而导入 commands。
	verifyAllLanguagesKey = "*"
)

// 哨兵错误用于分类被拒绝的 argv。它们对应 internal/eval/execcheck.go 中的哨兵，
// 使两份白名单在行为上保持一致。
var (
	errVerifyEmptyCommand    = errors.New("app: verify: empty command")
	errVerifyCommandDenied   = errors.New("app: verify: command is not allowlisted")
	errVerifyGoSubcommand    = errors.New("app: verify: go subcommand must be test or build")
	errVerifyBadPath         = errors.New("app: verify: executable path must be relative and stay inside the workspace")
	errVerifyShellMeta       = errors.New("app: verify: shell metacharacters are not allowed")
	errVerifyDangerousGoFlag = errors.New("app: verify: go flag can run arbitrary binaries")
)

// 本地的、选择启用的、安全有界的验证运行器。
//
// 这不是沙箱：没有操作系统隔离、没有网络限制、也没有用户切换。操作人员通过配置键或
// --verify-cmd 显式选择启用，安全性来自有界性而非隔离：
//
//  1. argv 白名单只接受 `go test|build ...` 或工作区相对的 .exe。shell、curl、绝对路径、
//     ".." 遍历、shell 元字符以及 -exec/-toolexec 都会被拒绝（validateVerifyArgv）。
//  2. argv 直接传给 os/exec；绝不涉及 shell。
//  3. Dir 是工作区根目录，子进程环境经过清理：剥离 GOFLAGS，使夹带的 -toolexec 不会通过环境重现，
//     并移除主机私有的会话变量，使验证命令派生的嵌套 agent 不会继承主机的会话身份。
//  4. 一个较短且有上限的 context 截止时间会杀掉失控进程。
//  5. 捕获的输出是 32KB 的 rune 安全尾部，返回前会脱敏。
//
// 把配置的命令当作可信代码，就像仓库已经运行的 Go 测试一样。除非在运行器上设置了 Verify，
// 否则没有任何东西会调用它。

// validateVerifyArgv 对应 internal/eval/execcheck.go 的 ExecCheck.Validate。
// 它是本地副本，因为那个文件属于另一条工作流；任一方改动时都要让两份策略保持同步。
//
// 允许：
//   - "go" 且 argv[1] == "test" 或 "build"
//   - 以 .exe 结尾、且不含 ".." 组件的相对路径
//
// 拒绝：绝对路径（包括 Windows 盘符前缀）、sh/bash/curl 之类的裸命令、路径遍历、
// 任何参数中的 shell 元字符，以及让 go test/build 启动任意二进制的 -exec/-toolexec 变体。
func validateVerifyArgv(argv []string) error {
	if len(argv) == 0 {
		return errVerifyEmptyCommand
	}
	command := strings.TrimSpace(argv[0])
	if command == "" {
		return errVerifyEmptyCommand
	}
	if strings.ContainsAny(command, verifyShellMeta) {
		return fmt.Errorf("%w: command %q", errVerifyShellMeta, command)
	}
	args := argv[1:]

	if command == "go" {
		if len(args) == 0 {
			return fmt.Errorf("%w: missing subcommand", errVerifyGoSubcommand)
		}
		switch args[0] {
		case "test", "build":
		default:
			return fmt.Errorf("%w: %q", errVerifyGoSubcommand, args[0])
		}
		return validateVerifyArgs(args)
	}

	if filepath.IsAbs(command) || strings.Contains(command, ":") {
		return fmt.Errorf("%w: %q", errVerifyBadPath, command)
	}
	for _, part := range strings.FieldsFunc(command, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if part == ".." {
			return fmt.Errorf("%w: %q", errVerifyBadPath, command)
		}
	}
	if !strings.EqualFold(filepath.Ext(command), ".exe") {
		return fmt.Errorf("%w: %q (expected \"go\" or a relative .exe path)", errVerifyCommandDenied, command)
	}
	return validateVerifyArgs(args)
}

func validateVerifyArgs(args []string) error {
	for _, arg := range args {
		if strings.ContainsAny(arg, verifyShellMeta) {
			return fmt.Errorf("%w: argument %q", errVerifyShellMeta, arg)
		}
		flag, _, _ := strings.Cut(arg, "=")
		switch flag {
		case "-exec", "--exec", "-toolexec", "--toolexec":
			return fmt.Errorf("%w: %q", errVerifyDangerousGoFlag, arg)
		}
	}
	return nil
}

// verifyEnvironment 返回验证子进程的环境。
//
// 对继承的环境应用两个独立的清理器：
//
//  1. proc.SanitizeEnv 丢弃主机私有的会话管道变量（ARGENT_SESSION_ID、ARGENT_RUN_ID、
//     ARGENT_PARENT_*、SUBAGENT_PARENT_*）。验证命令是可信代码，可能自身派生嵌套 agent；
//     没有这个过滤，嵌套 agent 可能把主机的会话状态误认为自己的并跨会话。
//     SanitizeEnv 最先应用，因为它自己读取 os.Environ()（其 `extra` 参数被原样追加且从不过滤）。
//  2. 丢弃 GOFLAGS，使配置的 `go test|build` 无法通过环境被传入
//     -toolexec（或其他任意二进制标志）。
//
// 其他一切——PATH、HOME、系统和操作人员变量——原样透传，使验证命令的行为不会退化。
func verifyEnvironment() []string {
	env := proc.SanitizeEnv(nil)
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "GOFLAGS") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// NewVerifyFunc 构建供 agent 循环使用的有界验证回调。cmd 是操作人员配置的 argv；
// 它在这里校验一次，回调复用被接受的副本。配置错误（空或被拒绝）的 cmd 会产生一个
// 始终报告 ok=false 的回调，因此不会注入任何反馈。
//
// changedPaths 绝不会被原样插值。对于携带全仓库 `./...` 目标的、在白名单中的 `go test|build` 命令，
// 回调使用 verifySafeTarget（清理 + 工作区检查 + 字符白名单）把该目标收窄为改动过的 Go 包。
// 每个保留下来的标记都由白名单生成，最终 argv 在执行前会再次校验，
// 因此模型选择的路径无法注入参数。无法变安全的路径会被跳过；若没有一个保留下来，
// 则配置的完整命令原样运行。非 go / .exe 命令永远不会被收窄——它们的参数约定未知。
//
// 进程正常结束时，回调返回 (label, exit==0, 脱敏后的输出, true)。派生失败或超时时返回
// (label, false, 脱敏后的错误, true)，使循环能把失败回灌给模型。
func NewVerifyFunc(cmd []string, timeoutMS int, root string) func(context.Context, []string) (string, bool, string, bool) {
	argv := append([]string(nil), cmd...)
	label := verifyLabel(argv)

	if err := validateVerifyArgv(argv); err != nil {
		reason := llm.RedactText("verify command rejected: " + err.Error())
		return func(context.Context, []string) (string, bool, string, bool) {
			return label, false, reason, false
		}
	}

	if timeoutMS <= 0 {
		timeoutMS = defaultVerifyTimeoutMS
	}
	if timeoutMS > maxVerifyTimeoutMS {
		timeoutMS = maxVerifyTimeoutMS
	}
	deadline := time.Duration(timeoutMS) * time.Millisecond
	if strings.TrimSpace(root) == "" {
		root = "."
	}

	return func(ctx context.Context, changedPaths []string) (string, bool, string, bool) {
		runArgv, _, _ := narrowVerifyArgv(argv, changedPaths, root)
		// 收窄后的 argv 只由白名单生成的标记重建；但仍要再次校验，
		// 以免未来对收窄逻辑的改动把非法参数偷带过构造时的白名单检查。
		if err := validateVerifyArgv(runArgv); err != nil {
			reason := llm.RedactText("verify command rejected: " + err.Error())
			return label, false, reason, false
		}

		name := runArgv[0]
		if name == "go" {
			// 在应用 Dir 之前解析 "go"，使工作区中放置的 go.exe 不会通过相对的 PATH 条目被选中。
			if found, err := exec.LookPath("go"); err == nil {
				if abs, err := filepath.Abs(found); err == nil {
					name = abs
				}
			}
		} else {
			// 校验保证它是相对且无遍历的 .exe；把它锚定到 root，即使 Dir 改变也能留在工作区内。
			name = filepath.Join(root, filepath.FromSlash(name))
		}

		runCtx, cancel := context.WithTimeout(ctx, deadline)
		defer cancel()

		command := exec.CommandContext(runCtx, name, runArgv[1:]...)
		command.Dir = root
		command.Env = verifyEnvironment()

		var out verifyTailBuffer
		command.Stdout = &out
		command.Stderr = &out
		err := command.Run()
		output := llm.RedactText(out.String())

		if err == nil {
			return label, true, output, true
		}

		// CommandContext 会杀掉超时进程，表现为 *exec.ExitError。先检查 context，
		// 使被杀掉被报告为超时，而不是普通的非零退出。
		timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
		ctxErr := ctx.Err()
		if !timedOut && ctxErr == nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				// 普通的非零退出是检查失败，仍是有效的结论。
				return label, false, output, true
			}
		}

		// 派生失败、超时或取消：报告脱敏后的错误，并附上任何部分输出。ok 保持 true，使循环能作反应。
		detail := err.Error()
		if timedOut {
			detail = fmt.Sprintf("verify command timed out after %s", deadline)
		} else if ctxErr != nil {
			detail = "verify command cancelled: " + ctxErr.Error()
		}
		if tail := out.String(); tail != "" {
			detail += "\n" + tail
		}
		return label, false, llm.RedactText(detail), true
	}
}

// verifyLabel 是配置检查的有界、脱敏名称。
func verifyLabel(argv []string) string {
	label := strings.Join(argv, " ")
	if runes := []rune(label); len(runes) > verifyLabelMax {
		label = string(runes[:verifyLabelMax]) + "..."
	}
	return llm.RedactText(label)
}

// verifySafeTarget 校验一条检查点路径，并把它映射为一个保守的 Go 包模式（"./dir"）。
// 它绝不原样返回模型控制的字节。该路径来自 MutationPaths 接缝，而后者由模型选择的工具参数提供，
// 因此这里的每一项检查都是纵深防御：
//
//   - 不含 shell 元字符、控制字节、NUL 或空白；
//   - 不含绝对路径、盘符前缀（"C:"）、UNC 根目录或前导分隔符；
//   - rune 白名单：Unicode 字母/数字（使 CJK 路径可用）加上 / \ _ - . ——其他一律拒绝；
//   - 词法 Clean 加上针对 root 的 filepath.Rel 限定检查；
//   - 只有 .go 文件和无扩展名的目录才成为目标。.go 文件收窄到其所属包；
//     其他文件类型无法收窄 Go 包集合，会被跳过。
//
// ok=false 表示"不信任该路径"——调用方会跳过它。
func verifySafeTarget(rootAbs, changed string) (string, bool) {
	p := strings.TrimSpace(changed)
	if p == "" || strings.ContainsAny(p, verifyShellMeta) {
		return "", false
	}
	// 在清理之前规范化 Windows 分隔符，使白名单和限定检查在每个平台上行为一致。
	p = strings.ReplaceAll(p, `\`, "/")
	if strings.HasPrefix(p, "/") || strings.Contains(p, ":") {
		return "", false
	}
	// 在 Clean 把遍历组件折叠为看似无害的根内路径之前，先拒绝它们
	// （例如 "a/../secret.go"）。
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", false
		}
	}
	for _, r := range p {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '/', r == '_', r == '-', r == '.':
			// 允许
		default:
			return "", false
		}
	}

	cleaned := filepath.Clean(filepath.FromSlash(p))
	slash := filepath.ToSlash(cleaned)
	if slash == "." || slash == ".." || strings.HasPrefix(slash, "../") ||
		strings.Contains(slash, "/../") || strings.HasSuffix(slash, "/..") {
		return "", false
	}
	abs, err := filepath.Abs(filepath.Join(rootAbs, cleaned))
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	relSlash := filepath.ToSlash(rel)

	dir := relSlash
	switch {
	case strings.HasSuffix(strings.ToLower(relSlash), ".go"):
		dir = filepath.ToSlash(filepath.Dir(filepath.FromSlash(relSlash)))
	case filepath.Ext(relSlash) != "":
		return "", false // 非 Go 文件：无法收窄 Go 构建
	}
	if dir == "." {
		dir = ""
	}
	return "./" + dir, true
}

// verifyNarrowTargets 把检查点路径映射为一个确定的（去重、排序的）安全 Go 包模式列表。
// 不安全或无关的路径会计入 skipped 并被省略——绝不会被插值。空的目标列表告诉调用方保留完整命令。
func verifyNarrowTargets(root string, changedPaths []string) (targets []string, skipped int) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, len(changedPaths)
	}
	seen := make(map[string]bool)
	for _, changed := range changedPaths {
		target, ok := verifySafeTarget(rootAbs, changed)
		if !ok {
			skipped++
			continue
		}
		if seen[target] {
			continue
		}
		seen[target] = true
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets, skipped
}

// narrowVerifyArgv 把白名单中的 `go test|build` 命令收窄到改动的 Go 包。两种模式：
//
//  1. 全仓库的 `./...`（或 `...`）目标被替换为安全目标——这是真正的
//     O(整个仓库) -> O(改动文件) 的收益；
//  2. 没有声明目标时，追加安全目标（否则裸的 `go test` / `go build` 表示"仅当前包"）。
//
// 对非 go 命令、.exe 验证命令（其 argv 约定未知）、没有路径能变安全，以及操作人员已经指定
// 显式包目标的情况，它都是空操作。在每种空操作情况下，配置的 argv 都按字节原样复制返回，
// 因此行为不变。
func narrowVerifyArgv(argv []string, changedPaths []string, root string) ([]string, bool, int) {
	out := append([]string(nil), argv...)
	if len(out) < 2 || out[0] != "go" || (out[1] != "test" && out[1] != "build") {
		return out, false, 0
	}
	targets, skipped := verifyNarrowTargets(root, changedPaths)
	if len(targets) == 0 {
		return out, false, skipped
	}
	for i := 2; i < len(out); i++ {
		if out[i] == "./..." || out[i] == "..." {
			next := make([]string, 0, len(out)-1+len(targets))
			next = append(next, out[:i]...)
			next = append(next, targets...)
			next = append(next, out[i+1:]...)
			return next, true, skipped
		}
	}
	for _, arg := range out[2:] {
		if !strings.HasPrefix(arg, "-") {
			// 非标志标记是操作人员选择的显式目标；覆盖它会静默改变配置的检查。
			return out, false, skipped
		}
	}
	return append(out, targets...), true, skipped
}

// verifyLangForms 把一个语言/扩展名键展开为用于匹配改动文件扩展名的等价写法。
// 同样的展开会应用于配置键和路径扩展名，使 "go"、".go"、"py"、".py" 和 "python"
// 能在中间汇合。前两种形式总是裸键及其带点的变体；其余是少量固定的别名。
func verifyLangForms(key string) []string {
	base := strings.ToLower(strings.TrimSpace(key))
	base = strings.TrimPrefix(base, ".")
	if base == "" {
		return nil
	}
	forms := []string{base, "." + base}
	switch base {
	case "py", "python":
		forms = append(forms, "python", ".python", "py", ".py")
	case "js", "javascript", "jsx", "mjs", "cjs":
		forms = append(forms, "javascript", ".javascript", "js", ".js", "jsx", ".jsx", "mjs", ".mjs", "cjs", ".cjs")
	case "ts", "typescript", "tsx":
		forms = append(forms, "typescript", ".typescript", "ts", ".ts", "tsx", ".tsx")
	case "rs", "rust":
		forms = append(forms, "rust", ".rust", "rs", ".rs")
	case "go", "golang":
		forms = append(forms, "golang", ".golang", "go", ".go")
	}
	return forms
}

// verifyAllLanguagesCmd 返回配置在 "*" 键下的兜底命令（commands.VerifyAllLanguages）。
// 查找会修剪键和值，使手写的 " * " 或空值行为合理。没有配置非空的兜底命令时 ok 为 false，
// 使单一的 VerifyCommand 字段仍作为回退。
func verifyAllLanguagesCmd(commands map[string]string) (string, bool) {
	for key, cmd := range commands {
		if strings.TrimSpace(key) == verifyAllLanguagesKey && strings.TrimSpace(cmd) != "" {
			return cmd, true
		}
	}
	return "", false
}

// canonicalVerifyCmd 把配置的命令字符串折叠为其 argv 键，使选择和执行对同一命令使用同一写法。
func canonicalVerifyCmd(cmd string) string {
	return strings.Join(strings.Fields(cmd), " ")
}

// verifyCommandIndex 一次性规范化配置的语言映射：键的每种写法都指向同一个命令字符串。
func verifyCommandIndex(commands map[string]string) map[string]string {
	index := make(map[string]string, len(commands)*2)
	for key, cmd := range commands {
		for _, form := range verifyLangForms(key) {
			index[form] = cmd
		}
	}
	return index
}

// selectVerifyArgvs 为 changedPaths 解析出有序、去重的待运行 argv 列表。
// 它刻意是纯的（不派生进程），使按语言分派无需执行命令即可单元测试。选择优先级从高到低：
//
//  1. 与路径扩展名匹配的语言专用命令；
//  2. "*" 兜底命令（commands.VerifyAllLanguages），aider 把它应用于没有专用条目的所有语言；
//  3. 单一的 VerifyCommand 字段，旧版回退。
//
// 只运行一个兜底命令：两者都设置时 "*" 优先于 VerifyCommand。无法选择任何内容时，
// 兜底命令运行一次，使缺失的检查点仍能验证某些东西。白名单稍后由 NewVerifyFunc 应用，
// 而不是这里。
func selectVerifyArgvs(commands map[string]string, fallback []string, changedPaths []string) [][]string {
	index := verifyCommandIndex(commands)
	allCmd, hasAll := verifyAllLanguagesCmd(commands)
	seen := map[string]bool{}
	var out [][]string
	add := func(argv []string) {
		if len(argv) == 0 {
			return
		}
		key := strings.Join(argv, " ")
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, argv)
	}

	// catchAll 是没有专用命令的路径的唯一回退："*" 命令优先于单一的 VerifyCommand 字段。
	catchAll := func() {
		if hasAll {
			add(strings.Fields(allCmd))
			return
		}
		add(append([]string(nil), fallback...))
	}

	for _, path := range changedPaths {
		base := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
		matched := false
		for _, form := range verifyLangForms(base) {
			if cmd, ok := index[form]; ok {
				add(strings.Fields(cmd))
				matched = true
				break
			}
		}
		if !matched {
			catchAll()
		}
	}
	if len(out) == 0 {
		// 没有检查点路径（或没有选中任何内容）：仍用兜底命令验证一次。
		catchAll()
	}
	return out
}

// newVerifyDispatchFunc 用按语言分派包装 NewVerifyFunc。每条不同的配置命令都会预先校验一次；
// 每次调用时，改动的路径决定执行哪些已预校验的运行器。结果会被聚合：
// 只有所有选中的命令都通过时，本次运行才算通过；只有某条选中的命令无法运行时 ok 才为 false。
func newVerifyDispatchFunc(commands map[string]string, fallback []string, timeoutMS int, root string) func(context.Context, []string) (string, bool, string, bool) {
	runners := map[string]func(context.Context, []string) (string, bool, string, bool){}
	for _, cmd := range commands {
		key := canonicalVerifyCmd(cmd)
		if _, ok := runners[key]; !ok {
			runners[key] = NewVerifyFunc(strings.Fields(cmd), timeoutMS, root)
		}
	}
	if len(fallback) > 0 {
		runners[canonicalVerifyCmd(strings.Join(fallback, " "))] = NewVerifyFunc(fallback, timeoutMS, root)
	}

	return func(ctx context.Context, changedPaths []string) (string, bool, string, bool) {
		argvs := selectVerifyArgvs(commands, fallback, changedPaths)
		if len(argvs) == 0 {
			return "", false, "", false
		}
		labels := make([]string, 0, len(argvs))
		outputs := make([]string, 0, len(argvs))
		allPassed, allOK := true, true
		for _, argv := range argvs {
			key := strings.Join(argv, " ")
			run := runners[key]
			if run == nil {
				// 选择只会从已预注册的命令中抽取；在这里构建一个回退，
				// 可防止未来键格式的改动引发 panic。
				run = NewVerifyFunc(argv, timeoutMS, root)
			}
			label, passed, output, ok := run(ctx, changedPaths)
			labels = append(labels, label)
			if output != "" {
				outputs = append(outputs, output)
			}
			allPassed = allPassed && passed
			allOK = allOK && ok
		}
		label := strings.Join(labels, "; ")
		if runes := []rune(label); len(runes) > verifyLabelMax {
			label = string(runes[:verifyLabelMax]) + "..."
		}
		return label, allPassed, strings.Join(outputs, "\n"), allOK
	}
}

// verifyConfigured 报告两种验证形式中是否有一种被启用。
func verifyConfigured(res Resolved) bool {
	return len(res.Cfg.VerifyCommand) > 0 || len(res.Cfg.VerifyCommands) > 0
}

// autoVerifyGoModExists 报告 root 下是否存在 go.mod 文件。它是自动验证激活的文件系统
// 接缝：测试可注入假的检查函数，生产路径使用这里的文件检查。目录不算有效的 go.mod。
func autoVerifyGoModExists(root string) bool {
	info, err := os.Stat(filepath.Join(root, "go.mod"))
	return err == nil && !info.IsDir()
}

// autoVerifyArgv 合成自动验证的 argv。仅当 auto 开启、两种显式验证配置都为空、
// 且工作区含 go.mod 时返回 `go test ./...`；其余情况返回 nil。
// 合成结果会再经过 validateVerifyArgv 白名单校验，失败时返回 nil（fail-closed）。
// goModExists 为 nil 时使用真实的文件系统检查。
func autoVerifyArgv(res Resolved, auto bool, goModExists func(string) bool) []string {
	if !auto || len(res.Cfg.VerifyCommands) > 0 || len(res.Cfg.VerifyCommand) > 0 {
		return nil
	}
	root := res.Root
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	if goModExists == nil {
		goModExists = autoVerifyGoModExists
	}
	if !goModExists(root) {
		return nil
	}
	argv := []string{"go", "test", "./..."}
	if err := validateVerifyArgv(argv); err != nil {
		return nil
	}
	return argv
}

// 除非操作人员显式配置了验证命令，否则 verifyFuncFor 返回 nil，
// 使未配置运行的 Options 逐字节不变（agent.Options.Verify == nil 会禁用整个循环）。
// auto 是可选开关（R25 opt-in auto_verify）：不传或传 false 时走原有分支，逐字节不变；
// 为 true 时在无显式配置且工作区含 go.mod 的情况下合成 `go test ./...` 回环。
// 只有旧版 VerifyCommand 被设置时，原样返回原有的固定回调；按语言映射则会增加
// 一个分派器，参考 agent 循环检查点提供的改动路径。
func verifyFuncFor(res Resolved, auto ...bool) func(context.Context, []string) (string, bool, string, bool) {
	return verifyFuncForAuto(res, len(auto) > 0 && auto[0])
}

// verifyFuncForAuto 是带显式 auto 开关的实现，供按 opt-in 注入的调用方使用。
// 显式配置（VerifyCommands / VerifyCommand）永远优先于自动合成。
func verifyFuncForAuto(res Resolved, auto bool) func(context.Context, []string) (string, bool, string, bool) {
	if len(res.Cfg.VerifyCommands) > 0 {
		return newVerifyDispatchFunc(res.Cfg.VerifyCommands, res.Cfg.VerifyCommand, res.Cfg.VerifyTimeoutMS, res.Root)
	}
	if argv := autoVerifyArgv(res, auto, nil); argv != nil {
		return NewVerifyFunc(argv, res.Cfg.VerifyTimeoutMS, res.Root)
	}
	if len(res.Cfg.VerifyCommand) == 0 {
		return nil
	}
	return NewVerifyFunc(res.Cfg.VerifyCommand, res.Cfg.VerifyTimeoutMS, res.Root)
}

// verifyReflectionsFor 解析每次运行的上限。功能关闭时返回零，使这个字段也与之前的 Options 完全一致。
func verifyReflectionsFor(res Resolved) int {
	if !verifyConfigured(res) {
		return 0
	}
	if n := res.Cfg.VerifyMaxReflections; n > 0 {
		return n
	}
	return agent.DefaultMaxVerifyReflections
}

// verifyTailBuffer 只保留写入它的最后 verifyOutputCap 字节，并前进到 rune 边界，
// 使尾部绝不会从一个 UTF-8 rune 的中间开始。
type verifyTailBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (w *verifyTailBuffer) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if w.buf.Len() > verifyOutputCap {
		data := w.buf.Bytes()
		drop := w.buf.Len() - verifyOutputCap
		for drop < len(data) && !utf8.RuneStart(data[drop]) {
			drop++
		}
		copy(data, data[drop:])
		// 截断到实际保留的字节数，而不是 verifyOutputCap。把 drop 推进越过被拆分的 rune
		// 会按相应字节数缩短尾部；若截断到完整上限，会在输出末尾暴露保留窗口之前的过期字节。
		w.buf.Truncate(len(data) - drop)
		w.truncated = true
	}
	return n, err
}

func (w *verifyTailBuffer) String() string {
	if !w.truncated {
		return w.buf.String()
	}
	return verifyTruncMarker + w.buf.String()
}
