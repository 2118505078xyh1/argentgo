package bash

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// shellBuiltinNames 是改变解释器状态或承载纯 shell 语义的内建命令。
var shellBuiltinNames = map[string]bool{
	"cd": true, "pwd": true, "echo": true, "export": true,
	"unset": true, "true": true, "false": true,
	// P1 控制流/函数配套内建。
	"local": true, "return": true, "break": true, "continue": true,
	"shift": true, ":": true,
	// 交互与脚本常用内建（纯 Go 实现，不依赖外部命令）。
	"read": true, "source": true, ".": true, "eval": true,
	"alias": true, "unalias": true, "history": true,
	"env": true, "which": true, "command": true, "exit": true, "time": true,
}

// coreutilNames 是能用标准库精确实现的 coreutils 子集。
// sed/awk/curl/find/xargs/sort 等刻意不在此列：它们在 interp 中走外部进程，
// 宁可交给真工具，也不做 glob 冒充正则之类的近似实现。
var coreutilNames = map[string]bool{
	"ls": true, "cat": true, "grep": true, "wc": true,
	"head": true, "tail": true, "mkdir": true, "rm": true,
	"printf": true, "test": true, "[": true,
	"touch": true, "cp": true, "mv": true, "tee": true,
}

// isBuiltin 分两组查询内建：shell 内建 + coreutils 子集。
func isBuiltin(name string) bool {
	return shellBuiltinNames[name] || coreutilNames[name] || sysBuiltinNames[name] || extraBuiltinNames[name] || extraBuiltinNames2[name]
}

// runBuiltin 执行内建命令，返回 (stdout, stderr, error)。
// 非零退出（如 false、grep 无匹配）通过 LastStatus 表达，不是 error。
func (it *Interp) runBuiltin(name string, args []string, stdin string) (string, string, error) {
	switch name {
	case "cd":
		return it.biCd(args)
	case "pwd":
		return it.biPwd(args)
	case "echo":
		return it.biEcho(args), "", nil
	case "export":
		return it.biExport(args)
	case "unset":
		return "", it.biUnset(args), nil
	case "true":
		it.LastStatus = 0
		return "", "", nil
	case "false":
		it.LastStatus = 1
		return "", "", nil
	case "local":
		// 函数作用域已由 callFunction 隔离，这里与 export 同义。
		return it.biExport(args)
	case "return":
		code := 0
		if len(args) > 0 {
			if n, err := strconv.Atoi(args[0]); err == nil {
				code = n
			}
		}
		it.LastStatus = code
		return "", "", &returnSignal{code: code}
	case "break":
		return "", "", errBreak
	case "continue":
		return "", "", errContinue
	case "shift":
		n := 1
		if len(args) > 0 {
			if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
				n = v
			}
		}
		if n > len(it.Args) {
			n = len(it.Args)
		}
		it.Args = it.Args[n:]
		it.LastStatus = 0
		return "", "", nil
	case ":":
		it.LastStatus = 0
		return "", "", nil
	case "read":
		return it.biRead(args, stdin)
	case "source", ".":
		return it.biSource(args)
	case "eval":
		return it.biEval(args)
	case "alias":
		return it.biAlias(args)
	case "unalias":
		return it.biUnalias(args)
	case "history":
		return it.biHistory(args)
	case "env":
		return it.biEnv(args, stdin)
	case "which":
		return it.biWhich(name, args)
	case "command":
		// command 无 -v/-p 时执行命令而非定位：绕过别名与函数，直接分派。
		// -v/-p 才走 biWhich 的查找逻辑。
		if len(args) > 0 && (args[0] == "-v" || args[0] == "-p") {
			return it.biWhich(name, args)
		}
		if len(args) == 0 {
			it.LastStatus = 0
			return "", "", nil
		}
		program, cmdArgs := args[0], args[1:]
		// 真实命令仍需经过安全谓词（execCall 对外建命令已审过 command 本身，
		// 这里审的是它即将执行的目标）。
		if !CurrentSafety(it.Ctx, it.Safety, strings.Join(args, " ")) {
			return "", "", fmt.Errorf("shell: command rejected by safety policy: %s", program)
		}
		if isBuiltin(program) {
			return it.runBuiltin(program, cmdArgs, stdin)
		}
		return it.runExternal(program, cmdArgs, stdin)
	case "exit":
		code := it.LastStatus
		if len(args) > 0 {
			if n, err := strconv.Atoi(args[0]); err == nil {
				code = n
			}
		}
		return "", "", &exitSignal{code: code}
	case "time":
		return it.biTime(args, stdin)
	}
	if coreutilNames[name] {
		return it.runCoreutil(name, args, stdin)
	}
	// 并行实现的扩展内建（sys2 / fs2 / sys / text2 / net / archive）。
	if out, errOut, err, ok := it.runSysBuiltin(name, args, stdin); ok {
		return out, errOut, err
	}
	if out, errOut, err, ok := it.runExtraBuiltin(name, args, stdin); ok {
		return out, errOut, err
	}
	if out, errOut, err, ok := it.runExtraBuiltin2(name, args, stdin); ok {
		return out, errOut, err
	}
	return "", fmt.Sprintf("shell: %s: command not found\n", name), nil
}

// resolvePath 把路径解析到工作区（Cwd）内，拒绝逃逸。所有文件类内建都走它。
func (it *Interp) resolvePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	// 兼容 Git Bash / MSYS 的 /d/... 写法，再交给标准库判绝对路径与边界检查。
	p = normalizeMSYSPath(p)
	base := it.Cwd
	if base == "" {
		base = "."
	}
	if !isAbs(p) {
		p = filepath.Join(base, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	// 相对路径基于当前目录解析，但"是否越界"始终以工作区根为基准。
	// 否则 cd 到子目录后 cd .. 会被误判为逃逸（实际是回到工作区内）。
	root := it.Workspace
	if root == "" {
		root = it.Cwd
	}
	if root == "" {
		root = "."
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace", p)
	}
	// 真实路径校验：解析符号链接/junction，阻断 .. 词法检查之外的逃逸。
	// 这是 shell 与 security 的自然结合点——路径边界由 security 统一定义。
	if err := security.CheckPathInRoots(abs, []string{rootAbs}); err != nil {
		return "", fmt.Errorf("path %q is outside the workspace: %w", p, err)
	}
	return abs, nil
}

func (it *Interp) biCd(args []string) (string, string, error) {
	target := it.Cwd
	printPath := false
	if len(args) > 0 {
		if args[0] == "-" {
			// cd -：切回上一次目录并打印新路径（与 bash 一致）。
			if it.OldPwd == "" {
				it.LastStatus = 1
				return "", "shell: cd: OLDPWD not set\n", nil
			}
			target = it.OldPwd
			printPath = true
		} else {
			target = args[0]
		}
	}
	abs, err := it.resolvePath(target)
	if err != nil {
		it.LastStatus = 1
		return "", fmt.Sprintf("shell: cd: %v\n", err), nil
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		it.LastStatus = 1
		return "", fmt.Sprintf("shell: cd: not a directory: %s\n", target), nil
	}
	it.OldPwd = it.Cwd
	it.Cwd = abs
	it.Vars["OLDPWD"] = it.OldPwd
	it.Vars["PWD"] = abs
	it.LastStatus = 0
	if printPath {
		return abs + "\n", "", nil
	}
	return "", "", nil
}

func (it *Interp) biPwd(args []string) (string, string, error) {
	it.LastStatus = 0
	return it.Cwd + "\n", "", nil
}

func (it *Interp) biEcho(args []string) string {
	// 简化：-n 抑制尾随换行（不做 -e 转义，避免语义膨胀）。
	noNewline := false
	if len(args) > 0 && args[0] == "-n" {
		noNewline = true
		args = args[1:]
	}
	s := strings.Join(args, " ")
	it.LastStatus = 0
	if noNewline {
		return s
	}
	return s + "\n"
}

func (it *Interp) biExport(args []string) (string, string, error) {
	for _, a := range args {
		if i := strings.IndexByte(a, '='); i > 0 {
			it.Vars[a[:i]] = a[i+1:]
		} else {
			it.Vars[a] = ""
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

func (it *Interp) biUnset(args []string) string {
	for _, a := range args {
		delete(it.Vars, a)
	}
	it.LastStatus = 0
	return ""
}
