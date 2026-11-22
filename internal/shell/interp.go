package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/proc"
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
	LastStatus int                       // $?
	Safety     func(command string) bool // 可选安全咨询谓词；返回 false 表示拒绝
	Timeout    time.Duration             // 外部命令超时；零值用 proc 默认
	Ctx        context.Context           // 取消上下文；nil 用 context.Background()
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
	out, _, err := it.execStmt(f.Stmt, "")
	return out, err
}

// execStmt 执行一条语句。stdin 是馈给该语句的输入（管道接力用）。
// 第二个返回值是该语句的 stdout。
func (it *Interp) execStmt(st *Stmt, stdin string) (string, string, error) {
	if st == nil {
		return "", "", nil
	}
	if u, ok := st.Cmd.(*UnsupportedCmd); ok {
		return "", "", &UnsupportedError{Kind: u.Kind}
	}
	if bin, ok := st.Cmd.(*BinaryCmd); ok {
		return it.execBinary(bin, stdin)
	}
	// 简单命令（可能带该语句级重定向）。
	return it.execCall(st, stdin)
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
		// 只把左段的 stdout 送入下一段：stderr 虽然按默认合并进可见输出，
		// 但它不属于 stdout 流，绝不能借管道污染右段的 stdin。
		pipeIn := leftStdout
		rightOut, rightStdout, err := it.execStmt(bin.Y, pipeIn)
		// 管道整体可见输出是右段的输出；左段输出不直接可见。
		return rightOut, rightStdout, err
	}
	return "", "", &UnsupportedError{Kind: "binary-op:" + bin.Op.String()}
}

// execCall 执行一个简单命令（含重定向）。
func (it *Interp) execCall(st *Stmt, stdin string) (string, string, error) {
	call, ok := st.Cmd.(*CallExpr)
	if !ok {
		return "", "", &UnsupportedError{Kind: "command"}
	}
	ctx := ExpandCtx{
		Vars: func(name string) (string, bool) {
			v, ok := it.Vars[name]
			return v, ok
		},
		LastStatus: it.LastStatus,
		Cwd:        it.Cwd,
		RunSubst: func(f *File) (string, error) {
			// 命令替换在逻辑上的子 shell 中执行：其退出状态不应污染外层命令的 $?。
			// 用局部保存/恢复把 it.LastStatus 的变更限制在替换体内（含出错路径）；
			// 变量与工作目录仍共享（本子集的既有取舍），仅隔离退出状态。
			saved := it.LastStatus
			defer func() { it.LastStatus = saved }()
			out, err := it.Run(f)
			return out, err
		},
	}

	var argv []string
	for _, w := range call.Args {
		fields, err := expandWord(w, ctx)
		if err != nil {
			return "", "", err
		}
		argv = append(argv, fields...)
	}
	if len(argv) == 0 {
		// 只有重定向的情况：视作空操作成功。
		if len(st.Redirs) > 0 {
			return "", "", nil
		}
		it.LastStatus = 0
		return "", "", nil
	}

	// 安全咨询谓词：拒绝则 fail-closed。
	// 经 CurrentSafety 统一判定：显式 opts.Safety 优先，未注入时回落到包级 oracle；
	// 两者皆无则默认放行。这里用展开后的真实 argv（而非原始命令行字符串）做咨询，
	// 使谓词看到的就是即将执行的程序与参数，更难被分词差异绕过。
	if !CurrentSafety(it.Safety, strings.Join(argv, " ")) {
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
	// shell 保留字（控制结构/函数等）在本子集不支持：fail-closed，明确拒绝，
	// 绝不把它们当外部命令尝试（那会产生误导性的 "command not found"）。
	if isReservedWord(program) {
		return "", "", &UnsupportedError{Kind: program}
	}
	args := argv[1:]
	var stdout, stderr string

	if isBuiltin(program) {
		stdout, stderr, err = it.runBuiltin(program, args, stdin)
	} else {
		stdout, stderr, err = it.runExternal(program, args, stdin)
	}
	if err != nil {
		return "", "", err
	}

	// 应用重定向，得到最终可见输出与供管道下一段使用的 stdout。
	visible, pipeOut, err := it.applyRedirects(stdout, stderr, redirs)
	if err != nil {
		return "", "", err
	}
	return visible, pipeOut, nil
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
		fields, err := expandWord(r.Target, ctx)
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
// 默认行为对齐系统 shell 的 exec.Cmd.CombinedOutput（tools_builtin_bash.go 的
// 默认 bash 路径）：stdout 与 stderr 合并进同一份可见输出，stderr 绝不静默丢弃。
// 由于 proc.Result 分开捕获两个流，这里采用与 runBashViaProc 的 Combined() 相同的
// 确定性顺序——stdout 段在前、stderr 段在后——作为实时交错的近似。
//
// 显式重定向只影响各自的流：
//   - 2>&1（RedirDupOut）：stderr 并入 stdout 流，既有可见输出也有管道输出；
//   - > file / >> file：仅 stdout 写入文件，未被重定向的 stderr 仍可见；
//     若同时出现 2>&1，则 stderr 也一并进文件；
//   - < file：在 execCall 中处理，此处忽略。
func (it *Interp) applyRedirects(stdout, stderr string, rs []expandedRedir) (string, string, error) {
	dupToStdout := false
	var outFile string
	var appendFile bool
	for _, r := range rs {
		switch r.op {
		case RedirDupOut:
			dupToStdout = true
		case RedirOut:
			outFile, appendFile = r.arg, false
		case RedirAppend:
			outFile, appendFile = r.arg, true
		}
		// RedirIn 在 execCall 阶段处理（stdin 已接线到内建与外部命令）；
		// 仅输出重定向在此生效。
	}

	// 默认合并：两个流都送往调用方（对齐 CombinedOutput）。
	visible := stdout + stderr
	// 管道只传 stdout 流；2>&1 把 stderr 也并进该流。
	pipeOut := stdout
	if dupToStdout {
		pipeOut += stderr
	}

	if outFile != "" {
		// 输出目标同样先经 resolvePath 校验，拒绝 ../ 逃逸或经链接
		// 落到工作区外的写入；随后用解析出的 abs 路径打开文件。
		path, err := it.resolvePath(outFile)
		if err != nil {
			return "", "", fmt.Errorf("shell: redirect to %q: %w", outFile, err)
		}
		flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if appendFile {
			flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		f, err := os.OpenFile(path, flag, 0o644)
		if err != nil {
			return "", "", fmt.Errorf("shell: redirect to %q: %w", outFile, err)
		}
		defer f.Close()
		fileData := stdout
		if dupToStdout {
			fileData += stderr // 2>&1：stderr 跟随 stdout 进文件
		}
		if _, err := f.WriteString(fileData); err != nil {
			return "", "", err
		}
		// stdout 已进文件，管道无 stdout 可传；未重定向的 stderr 仍可见。
		pipeOut = ""
		visible = ""
		if !dupToStdout {
			visible = stderr
		}
		return visible, pipeOut, nil
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
		"case", "esac", "do", "done", "in", "function", "select", "time",
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

func isAbs(p string) bool { return strings.HasPrefix(p, "/") || (len(p) >= 3 && p[1] == ':') }
