package bash

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"example.com/argentgo/internal/proc"
)

// 执行期哨兵错误。
var (
	ErrUnsupported     = errors.New("shell: unsupported construct")
	ErrCommandNotFound = errors.New("shell: command not found")
	ErrBadRedirect     = errors.New("shell: bad redirect")
)

// UnsupportedError 携带具体不支持的构造名。
type UnsupportedError struct{ Kind string }

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("shell: unsupported construct: %s", e.Kind)
}
func (e *UnsupportedError) Unwrap() error { return ErrUnsupported }

// Interp 是一次 shell 解释执行的运行时状态。
// 依赖（安全咨询、命令替换执行）通过字段注入，保持零外部耦合。
type Interp struct {
	Vars       map[string]string         // 变量表（导出的与内部的一致处理）
	Cwd        string                    // 当前工作目录
	Workspace  string                    // 工作区根目录：路径边界的基准，不随 cd 改变
	LastStatus int                       // $?
	Safety     func(command string) bool // 可选安全咨询谓词；返回 false 表示拒绝
	Timeout    time.Duration             // 外部命令超时；零值用 proc 默认
	Ctx        context.Context           // 取消上下文；nil 用 context.Background()
	Funcs      map[string]*FuncDecl      // 已定义的函数表
	Args       []string                  // 当前位置参数（$1..$9 / $@ / $#）
	OldPwd     string                    // 上一次工作目录（cd -）
	Aliases    map[string]string         // 命令别名（alias/unalias）
	History    []string                  // 本次会话已执行命令（history）
	depth      int                       // 当前解释递归深度（Run/函数/命令替换共用），防自引用无限递归
}

// maxInterpDepth 是解释递归的安全上限：source/eval 自引用、函数递归、
// 深层命令替换都不得无限增长调用栈。
const maxInterpDepth = 100

// 控制流哨兵：break/continue 在循环边界消费，returnSignal 在函数边界消费。
// 它们作为 error 沿 execStmt 链向上传播，使嵌套结构能正确短路。
var (
	errBreak    = errors.New("shell: break")
	errContinue = errors.New("shell: continue")
)

type returnSignal struct{ code int }

func (*returnSignal) Error() string { return "shell: return" }

func isFlowSignal(err error) bool {
	var rs *returnSignal
	return errors.Is(err, errBreak) || errors.Is(err, errContinue) || errors.As(err, &rs)
}

func (it *Interp) ctx() context.Context {
	if it.Ctx == nil {
		return context.Background()
	}
	return it.Ctx
}

// Run 执行解析后的命令，返回合并后的输出与错误。
// 非零退出是正常结果（输出 + nil error），与上层 bash 工具语义一致。
func (it *Interp) Run(f *File) (string, error) {
	if f == nil || f.Stmt == nil {
		return "", nil
	}
	it.depth++
	defer func() { it.depth-- }()
	if it.depth > maxInterpDepth {
		return "", fmt.Errorf("shell: 解释递归深度超过 %d（疑似 source/eval 自引用）", maxInterpDepth)
	}
	// 未显式指定工作区时，以启动目录为根；cd 只改变 Cwd，不改变边界。
	if it.Workspace == "" {
		it.Workspace = it.Cwd
	}
	out, _, err := it.execStmt(f.Stmt, "")
	// exit 在顶层终止脚本，退出码写入 $?，不再向上抛错。
	var es *exitSignal
	if errors.As(err, &es) {
		it.LastStatus = es.code
		err = nil
	}
	return out, err
}

// execStmt 执行一条语句。stdin 是馈给该语句的输入（管道接力用）。
// 第二个返回值是该语句的 stdout。
func (it *Interp) execStmt(st *Stmt, stdin string) (string, string, error) {
	if st == nil {
		return "", "", nil
	}
	switch c := st.Cmd.(type) {
	case *UnsupportedCmd:
		return "", "", &UnsupportedError{Kind: c.Kind}
	case *BinaryCmd:
		return it.execBinary(c, stdin)
	case *IfCmd:
		return it.execIf(c, stdin)
	case *ForCmd:
		return it.execFor(c, stdin)
	case *WhileCmd:
		return it.execWhile(c, stdin)
	case *CaseCmd:
		return it.execCase(c, stdin)
	case *FuncDecl:
		return it.execFuncDecl(c, stdin)
	case *Block:
		return it.execBlock(c, stdin)
	}
	// 简单命令（可能带该语句级重定向）。
	return it.execCall(st, stdin)
}

// expandCtx 构造当前上下文的参数展开视图，供控制结构与简单命令共用。
func (it *Interp) expandCtx() ExpandCtx {
	return ExpandCtx{
		Vars: func(name string) (string, bool) {
			v, ok := it.Vars[name]
			return v, ok
		},
		LastStatus: it.LastStatus,
		RunSubst: func(f *File) (string, error) {
			// 命令替换在逻辑上的子 shell 中执行：任何状态变更（变量、工作目录、
			// 位置参数、函数、别名、历史、退出码）都不得泄漏到父解释器。
			// 与 bash 一致：子 shell 继承父的可见状态，但其修改仅作用于自身。
			savedVars, savedArgs := it.Vars, it.Args
			savedCwd, savedOldPwd, savedStatus := it.Cwd, it.OldPwd, it.LastStatus
			savedFuncs, savedAliases := it.Funcs, it.Aliases
			savedHistory := it.History
			it.Vars = copyVars(savedVars)
			it.Args = append([]string(nil), savedArgs...)
			it.Funcs = copyFuncs(savedFuncs)
			it.Aliases = copyAliases(savedAliases)
			it.History = append([]string(nil), savedHistory...)
			out, err := it.Run(f)
			it.Vars, it.Args = savedVars, savedArgs
			it.Cwd, it.OldPwd, it.LastStatus = savedCwd, savedOldPwd, savedStatus
			it.Funcs, it.Aliases = savedFuncs, savedAliases
			it.History = savedHistory
			return out, err
		},
		Cwd:  it.Cwd,
		Args: it.Args,
	}
}

// execIf 执行 if/elif/else：按条件选择第一条命中的分支。
func (it *Interp) execIf(c *IfCmd, stdin string) (string, string, error) {
	var out strings.Builder
	runCond := func(cond *Stmt) (bool, error) {
		if _, _, err := it.execStmt(cond, ""); err != nil {
			return false, err
		}
		return it.LastStatus == 0, nil
	}
	ok, err := runCond(c.Cond)
	if err != nil {
		return "", "", err
	}
	runBody := func(body *Stmt) (string, string, error) {
		o, so, e := it.execStmt(body, stdin)
		out.WriteString(o)
		return out.String(), so, e
	}
	if ok {
		return runBody(c.Then)
	}
	for _, el := range c.Elifs {
		ok, err := runCond(el.Cond)
		if err != nil {
			return out.String(), "", err
		}
		if ok {
			return runBody(el.Body)
		}
	}
	if c.Else != nil {
		return runBody(c.Else)
	}
	it.LastStatus = 0
	return out.String(), out.String(), nil
}

// execWhile 执行 while/until 循环；break/continue 在此消费，return 向上传播。
// maxLoopGuard 是 while 循环的安全上限，防止失控脚本挂死解释器。
const maxLoopGuard = 100000

func (it *Interp) execWhile(c *WhileCmd, stdin string) (string, string, error) {
	var out, stdout strings.Builder
	condIn, bodyIn := stdin, stdin
	lastStatus := 0
	guard := 0
	for ; guard < maxLoopGuard; guard++ {
		if _, _, err := it.execStmt(c.Cond, condIn); err != nil {
			return out.String(), stdout.String(), err
		}
		condIn = "" // stdin 只消费一次
		cond := it.LastStatus == 0
		if c.Until {
			cond = !cond
		}
		if !cond {
			break
		}
		o, so, err := it.execStmt(c.Body, bodyIn)
		bodyIn = ""
		out.WriteString(o)
		stdout.WriteString(so)
		lastStatus = it.LastStatus
		if err != nil {
			if errors.Is(err, errBreak) {
				break
			}
			if errors.Is(err, errContinue) {
				continue
			}
			return out.String(), stdout.String(), err
		}
	}
	if guard >= maxLoopGuard {
		return out.String(), stdout.String(), fmt.Errorf("shell: while 循环超过 %d 次安全上限", maxLoopGuard)
	}
	it.LastStatus = lastStatus
	return out.String(), stdout.String(), nil
}

// execFor 执行 for 循环；无 in 词表时遍历位置参数。
func (it *Interp) execFor(c *ForCmd, stdin string) (string, string, error) {
	ctx := it.expandCtx()
	var items []string
	for _, w := range c.Items {
		fields, err := expandWord(w, ctx)
		if err != nil {
			return "", "", err
		}
		items = append(items, fields...)
	}
	if len(c.Items) == 0 {
		items = append(items, it.Args...)
	}
	var out, stdout strings.Builder
	bodyIn := stdin
	lastStatus := 0
	for _, item := range items {
		it.Vars[c.Var] = item
		o, so, err := it.execStmt(c.Body, bodyIn)
		bodyIn = ""
		out.WriteString(o)
		stdout.WriteString(so)
		lastStatus = it.LastStatus
		if err != nil {
			if errors.Is(err, errBreak) {
				break
			}
			if errors.Is(err, errContinue) {
				continue
			}
			return out.String(), stdout.String(), err
		}
	}
	it.LastStatus = lastStatus
	return out.String(), stdout.String(), nil
}

// execCase 执行 case：模式用 path.Match（支持 * ? [），首个命中分支执行后退出。
// 用 path 而非 filepath：case 的 * 应能匹配含路径分隔符的值（与 bash 一致），
// filepath.Match 会把分隔符视为 * 不可跨越的边界。
func (it *Interp) execCase(c *CaseCmd, stdin string) (string, string, error) {
	ctx := it.expandCtx()
	fields, err := expandWord(c.Word, ctx)
	if err != nil {
		return "", "", err
	}
	val := ""
	if len(fields) > 0 {
		val = fields[0]
	}
	for _, cl := range c.Clauses {
		for _, pw := range cl.Patterns {
			pats, err := expandWord(pw, ctx)
			if err != nil {
				return "", "", err
			}
			for _, pat := range pats {
				matched, e := path.Match(pat, val)
				if e == nil && matched {
					o, so, e2 := it.execStmt(cl.Body, stdin)
					return o, so, e2
				}
			}
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

// execFuncDecl 注册函数定义（本身无输出）。
func (it *Interp) execFuncDecl(c *FuncDecl, stdin string) (string, string, error) {
	if it.Funcs == nil {
		it.Funcs = make(map[string]*FuncDecl)
	}
	it.Funcs[c.Name] = c
	it.LastStatus = 0
	return "", "", nil
}

// execBlock 执行花括号复合命令。
func (it *Interp) execBlock(c *Block, stdin string) (string, string, error) {
	if c.Body == nil {
		return "", "", nil
	}
	return it.execStmt(c.Body, stdin)
}

// execBinary 处理 ; && || | 四种二元命令。
func (it *Interp) execBinary(bin *BinaryCmd, stdin string) (string, string, error) {
	switch bin.Op {
	case OpSeq:
		// ';'：无论左边成败都执行右边，最终状态取右边。
		leftOut, _, err := it.execStmt(bin.X, stdin)
		if err != nil {
			return leftOut, "", err
		}
		rightOut, rightStdout, err := it.execStmt(bin.Y, "")
		return leftOut + rightOut, rightStdout, err

	case OpAnd:
		leftOut, _, err := it.execStmt(bin.X, stdin)
		if err != nil {
			return leftOut, "", err
		}
		if it.LastStatus != 0 {
			return leftOut, "", nil // 左侧失败，短路
		}
		rightOut, rightStdout, err := it.execStmt(bin.Y, "")
		return leftOut + rightOut, rightStdout, err

	case OpOr:
		leftOut, _, err := it.execStmt(bin.X, stdin)
		if err != nil {
			return leftOut, "", err
		}
		if it.LastStatus == 0 {
			return leftOut, "", nil // 左侧成功，短路
		}
		rightOut, rightStdout, err := it.execStmt(bin.Y, "")
		return leftOut + rightOut, rightStdout, err

	case OpPipe:
		// 字符串接力：左段的 stdout 成为右段的 stdin（非流式，诚实取舍）。
		// 默认退出码取最后一段：execStmt(Y) 会把 Y 的状态写回 it.LastStatus。
		// 本子集没有 set -o pipefail，因此不实现"最左非零"分支。
		leftCombined, leftStdout, err := it.execStmt(bin.X, stdin)
		if err != nil {
			return leftCombined, "", err
		}
		// 只把左段的 stdout 送入下一段：stderr 不属于 stdout 流，
		// 绝不能借管道污染右段的 stdin。
		pipeIn := leftStdout
		rightOut, rightStdout, err := it.execStmt(bin.Y, pipeIn)
		// 与 bash 一致：左段 stderr 不进管道但保持可见（如 `ls /missing | cat`）。
		// leftCombined 是 stdout+stderr 的合并视图，其超出纯 stdout 的部分即 stderr。
		leftStderr := ""
		if strings.HasPrefix(leftCombined, leftStdout) {
			leftStderr = leftCombined[len(leftStdout):]
		}
		return leftStderr + rightOut, rightStdout, err
	}
	return "", "", &UnsupportedError{Kind: "binary-op:" + bin.Op.String()}
}

// execCall 执行一个简单命令（含重定向）。
func (it *Interp) execCall(st *Stmt, stdin string) (string, string, error) {
	call, ok := st.Cmd.(*CallExpr)
	if !ok {
		return "", "", &UnsupportedError{Kind: "command"}
	}
	ctx := it.expandCtx()

	var argv []string
	for _, w := range call.Args {
		fields, err := expandWord(w, ctx)
		if err != nil {
			return "", "", err
		}
		argv = append(argv, fields...)
	}

	// 前导赋值 NAME=VALUE：独立出现时是赋值语句，与命令同现时是临时环境。
	// 展开只取首个字段（赋值右侧不参与 glob；分词差异留待后续补齐）。
	type assignVar struct {
		name, newVal, oldVal string
		oldSet               bool
	}
	var assigns []assignVar
	for _, w := range call.Assigns {
		fields, err := expandWord(w, ctx)
		if err != nil {
			return "", "", err
		}
		if len(fields) == 0 {
			continue
		}
		s := fields[0]
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			continue
		}
		assigns = append(assigns, assignVar{name: s[:eq], newVal: s[eq+1:]})
	}
	applyAssigns := func() {
		for i := range assigns {
			v, ok := it.Vars[assigns[i].name]
			assigns[i].oldVal, assigns[i].oldSet = v, ok
			it.Vars[assigns[i].name] = assigns[i].newVal
		}
	}
	restoreAssigns := func() {
		for _, a := range assigns {
			if a.oldSet {
				it.Vars[a.name] = a.oldVal
			} else {
				delete(it.Vars, a.name)
			}
		}
	}

	if len(argv) == 0 {
		if len(assigns) > 0 {
			applyAssigns() // 独立赋值：持久化到变量表
			it.LastStatus = 0
		}
		// 只有重定向的情况：视作空操作成功。
		return "", "", nil
	}
	// 命令前缀赋值：命令执行期间生效，返回后恢复。
	applyAssigns()
	defer restoreAssigns()

	// 安全咨询谓词：拒绝则 fail-closed。
	// 经 CurrentSafety 统一判定：显式 opts.Safety 优先，未注入时回落到 ctx 中
	// 本次运行注入的 oracle；两者皆无则默认放行。这里用展开后的真实 argv
	// （而非原始命令行字符串）做咨询，使谓词看到的就是即将执行的程序与参数，
	// 更难被分词差异绕过。
	if !CurrentSafety(it.Ctx, it.Safety, strings.Join(argv, " ")) {
		return "", "", fmt.Errorf("shell: command rejected by safety policy: %s", argv[0])
	}

	// 展开重定向目标（不分词、不 glob）。
	redirs, err := it.expandRedirects(st.Redirs, ctx)
	if err != nil {
		return "", "", err
	}

	// 输入重定向 '<' 覆盖管道传入的 stdin。
	// 目标先经 resolvePath 解析到工作区内（含符号链接/junction 校验），
	// 阻断借 ../ 或工作区内链接读取工作区外的文件。
	for _, r := range redirs {
		if r.op == RedirIn && r.arg != "" {
			path, err := it.resolvePath(r.arg)
			if err != nil {
				return "", "", fmt.Errorf("shell: redirect from %q: %w", r.arg, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", "", fmt.Errorf("shell: redirect from %q: %w", r.arg, err)
			}
			stdin = string(data)
		}
	}

	program := argv[0]
	// 残留保留字（parser 未分派的构造）fail-closed，绝不尝试当外部命令执行。
	if isReservedWord(program) {
		return "", "", &UnsupportedError{Kind: program}
	}
	args := argv[1:]
	var stdout, stderr string

	// 别名先于函数/内建展开。展开时临时摘掉同名别名，避免 alias ls='ls -l'
	// 这类自引用无限递归（bash 的别名展开也只做一轮）。
	if aliasBody, ok := it.Aliases[program]; ok {
		delete(it.Aliases, program)
		defer func() { it.Aliases[program] = aliasBody }()
		expanded := strings.Join(append([]string{aliasBody}, args...), " ")
		f, e := parseCommandString(expanded)
		if e != nil {
			it.LastStatus = 2
			return "", "", e
		}
		visible, pipeStdout, e := it.execStmt(f.Stmt, stdin)
		stderr := visible
		if strings.HasPrefix(visible, pipeStdout) {
			stderr = visible[len(pipeStdout):]
		}
		stdout, err = pipeStdout, e
		// 别名展开后的命令已消费了 stdin，但重定向仍需在此统一应用。
		return it.finishExecCall(stdout, stderr, err, redirs)
	}

	// 函数优先于内建与外部命令（与 bash 一致）。
	if fn, ok := it.Funcs[program]; ok {
		stdout, stderr, err = it.callFunction(fn, args, stdin)
	} else if isBuiltin(program) {
		stdout, stderr, err = it.runBuiltin(program, args, stdin)
	} else {
		stdout, stderr, err = it.runExternal(program, args, stdin)
	}
	if err != nil {
		return "", "", err
	}

	// 应用重定向，得到最终可见输出与供管道下一段使用的 stdout。
	return it.finishExecCall(stdout, stderr, err, redirs)
}

// finishExecCall 统一应用输出重定向，得到 (可见输出, 供管道的纯 stdout, error)。
// 别名、函数、内建、外部命令四条路径都经由此处，确保重定向语义一致。
func (it *Interp) finishExecCall(stdout, stderr string, callErr error, redirs []expandedRedir) (string, string, error) {
	if callErr != nil {
		return "", "", callErr
	}
	visible, pipeOut, err := it.applyRedirects(stdout, stderr, redirs)
	if err != nil {
		return "", "", err
	}
	return visible, pipeOut, nil
}

// callFunction 在隔离的变量作用域中执行函数体。
// 作用域取舍（P1）：函数内的普通赋值不泄漏到调用方，只有 cd 改变的工作目录
// 与显式 export 出的变量（通过上层工具环境）保持——这与 bash 的"变量默认全局"
// 不同，但在编码 agent 场景下更安全、可预测。return 在函数边界被消费。
func (it *Interp) callFunction(fn *FuncDecl, args []string, stdin string) (string, string, error) {
	it.depth++
	defer func() { it.depth-- }()
	if it.depth > maxInterpDepth {
		return "", "", fmt.Errorf("shell: 函数递归深度超过 %d（疑似 %s 自递归）", maxInterpDepth, fn.Name)
	}
	savedVars, savedArgs := it.Vars, it.Args
	it.Vars = copyVars(savedVars)
	it.Args = append([]string(nil), args...)

	visible, stdout, err := it.execStmt(fn.Body, stdin)
	// execStmt 的第二个返回值是纯 stdout（供管道）；第一个是 stdout+stderr 的可见合并。
	// 二者之差即 stderr，必须单独保留，避免函数参与管道时 stderr 污染下一段 stdin。
	stderr := visible
	if strings.HasPrefix(visible, stdout) {
		stderr = visible[len(stdout):]
	}
	status := it.LastStatus
	if err != nil {
		var rs *returnSignal
		if errors.As(err, &rs) {
			status = rs.code
			err = nil
		}
	}
	it.Vars, it.Args = savedVars, savedArgs
	it.LastStatus = status
	return stdout, stderr, err
}

// expandedRedir 是展开后的重定向。
type expandedRedir struct {
	op  RedirOp
	n   int
	arg string
}

func (it *Interp) expandRedirects(rs []*Redirect, ctx ExpandCtx) ([]expandedRedir, error) {
	var out []expandedRedir
	for _, r := range rs {
		er := expandedRedir{op: r.Op, n: r.N}
		if r.Op == RedirDupOut {
			out = append(out, er)
			continue
		}
		if r.Target == nil {
			return nil, ErrBadRedirect
		}
		// 重定向目标不做路径名展开（bash 语义）：复制 ctx 并禁用 glob。
		targetCtx := ctx
		targetCtx.NoGlob = true
		fields, err := expandWord(r.Target, targetCtx)
		if err != nil {
			return nil, err
		}
		if len(fields) != 1 {
			return nil, fmt.Errorf("%w: ambiguous redirect", ErrBadRedirect)
		}
		er.arg = fields[0]
		out = append(out, er)
	}
	return out, nil
}

// applyRedirects 应用重定向，返回 (可见输出, 供管道下一段的 stdout, error)。
//
// 默认行为对齐系统 shell 的 exec.Cmd.CombinedOutput：stdout 与 stderr 合并进
// 同一份可见输出，stderr 绝不静默丢弃；顺序固定为 stdout 段在前、stderr 段在后。
//
// 显式重定向按 fd 分别处理：
//   - 2> file / 2>> file：stderr 写入文件，stdout 仍可见并进入管道；
//   - > file / >> file：stdout 写入文件，未被重定向的 stderr 仍可见；
//   - 2>&1（RedirDupOut）：stderr 并入 stdout 流（含管道与文件）；
//   - &> file（RedirAll）：stdout 与 stderr 一起写入文件；
//   - < file：在 execCall 中处理，此处忽略。
func (it *Interp) applyRedirects(stdout, stderr string, rs []expandedRedir) (string, string, error) {
	dupToStdout := false
	var outFile, errFile string
	var outAppend, errAppend bool

	for _, r := range rs {
		switch r.op {
		case RedirDupOut:
			dupToStdout = true
		case RedirAll:
			outFile, outAppend = r.arg, false
			dupToStdout = true // &> 等价于 > file 2>&1
		case RedirOut:
			if r.n == 2 {
				errFile, errAppend = r.arg, false
			} else {
				outFile, outAppend = r.arg, false
			}
		case RedirAppend:
			if r.n == 2 {
				errFile, errAppend = r.arg, true
			} else {
				outFile, outAppend = r.arg, true
			}
		}
		// RedirIn 在 execCall 阶段处理（stdin 已接线到内建与外部命令）。
	}

	// 合并流：2>&1 或 &> 让 stderr 跟随 stdout。
	stdData, errData := stdout, stderr
	if dupToStdout {
		stdData = stdout + stderr
		errData = ""
	}

	// writeRedirect 统一做路径校验（拒绝 ../ 逃逸与符号链接逃逸）后写文件。
	writeRedirect := func(path, data string, appendMode bool) error {
		abs, err := it.resolvePath(path)
		if err != nil {
			return fmt.Errorf("shell: redirect to %q: %w", path, err)
		}
		flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if appendMode {
			flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		f, err := os.OpenFile(abs, flag, 0o644)
		if err != nil {
			return fmt.Errorf("shell: redirect to %q: %w", path, err)
		}
		defer f.Close()
		_, err = f.WriteString(data)
		return err
	}

	visible, pipeOut := "", ""
	if outFile != "" {
		if err := writeRedirect(outFile, stdData, outAppend); err != nil {
			return "", "", err
		}
	} else {
		visible += stdData
		pipeOut = stdData
	}
	if errFile != "" {
		if err := writeRedirect(errFile, errData, errAppend); err != nil {
			return "", "", err
		}
	} else {
		visible += errData
	}
	return visible, pipeOut, nil
}

// runExternal 用 internal/proc 执行外部程序（进程组管理、UTF-8、输出上限）。
func (it *Interp) runExternal(program string, args []string, stdin string) (string, string, error) {
	path, err := exec.LookPath(program)
	if err != nil {
		it.LastStatus = 127
		return "", fmt.Sprintf("shell: %s: command not found\n", program), nil
	}
	c := proc.Command{
		Path:     path,
		Args:     args,
		Dir:      it.Cwd,
		Env:      envSlice(it.Vars),
		Timeout:  it.Timeout,
		KillTree: true,
		NoWindow: true,
	}
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	res, err := proc.Run(it.ctx(), c)
	if err != nil {
		return "", "", err
	}
	it.LastStatus = res.ExitCode
	return res.Stdout, res.Stderr, nil
}

// isReservedWord 报告 name 是否为 shell 控制结构/函数保留字（本子集一律不支持）。
func isReservedWord(name string) bool {
	switch name {
	case "for", "while", "until", "if", "then", "else", "elif", "fi",
		"case", "esac", "do", "done", "in", "function", "select",
		"{", "}", "!":
		return true
	}
	return false
}

func envSlice(vars map[string]string) []string {
	out := make([]string, 0, len(vars))
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	return out
}

// isAbs 用标准库判定绝对路径：跨平台正确，且不会把 "a:b" 这类字符串
// （旧实现仅看 p[1]==':' 会误判为盘符绝对路径）错误放行。
func isAbs(p string) bool { return filepath.IsAbs(p) }
