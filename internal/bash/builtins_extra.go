package bash

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// biTime 测量一条命令的执行耗时：解析后续参数为命令并执行，
// 把耗时与用户/系统时间写到 stderr（对齐 GNU time 的输出位置）。
func (it *Interp) biTime(args []string, stdin string) (string, string, error) {
	if len(args) == 0 {
		it.LastStatus = 0
		return "", "用法: time <命令>\n", nil
	}
	cmd := strings.Join(args, " ")
	f, err := parseCommandString(cmd)
	if err != nil {
		it.LastStatus = 2
		return "", err.Error() + "\n", nil
	}
	start := time.Now()
	out, _, runErr := it.execStmt(f.Stmt, stdin)
	elapsed := time.Since(start)
	real := fmt.Sprintf("real\t%.3fs\n", elapsed.Seconds())
	// 内建执行无独立 user/sys 进程计时，给出 0 并说明。
	detail := "user\t0.000s\nsys\t0.000s\n（内建执行，无独立进程计时）\n"
	return out, real + detail, runErr
}

// exitSignal 由 exit 内建产生，沿 execStmt 链传播到 Interp.Run 顶层消费。
type exitSignal struct{ code int }

func (*exitSignal) Error() string { return "shell: exit" }

// biRead 从 stdin 读一行，按空白分词赋给变量（无变量名时存入 REPLY）。
// 工具场景下 stdin 通常来自管道或 < 重定向。
func (it *Interp) biRead(args []string, stdin string) (string, string, error) {
	// 无输入（EOF）时按 bash 语义返回非零：这是 while read 循环能正常终止的前提。
	if stdin == "" {
		it.LastStatus = 1
		return "", "", nil
	}
	line, _, _ := strings.Cut(stdin, "\n")
	fs := strings.Fields(line)
	if len(args) == 0 {
		it.Vars["REPLY"] = line
	} else {
		for i, name := range args {
			v := ""
			if i < len(fs) {
				v = fs[i]
			}
			it.Vars[name] = v
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

// biSource 读取脚本文件并在当前解释器作用域执行（变量/函数对调用方可见）。
func (it *Interp) biSource(args []string) (string, string, error) {
	if len(args) == 0 {
		it.LastStatus = 2
		return "", "shell: source: 缺少文件名\n", nil
	}
	path, err := it.resolvePath(args[0])
	if err != nil {
		it.LastStatus = 1
		return "", fmt.Sprintf("shell: source: %v\n", err), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		it.LastStatus = 1
		return "", fmt.Sprintf("shell: source: %v\n", err), nil
	}
	f, err := parseCommandString(string(data))
	if err != nil {
		it.LastStatus = 2
		return "", err.Error() + "\n", nil
	}
	out, err := it.Run(f)
	return out, "", err
}

// biEval 把参数拼成命令字符串后解析执行。
func (it *Interp) biEval(args []string) (string, string, error) {
	f, err := parseCommandString(strings.Join(args, " "))
	if err != nil {
		it.LastStatus = 2
		return "", err.Error() + "\n", nil
	}
	out, err := it.Run(f)
	return out, "", err
}

// biAlias 定义或列出别名。别名展开在 execCall 中完成。
func (it *Interp) biAlias(args []string) (string, string, error) {
	if it.Aliases == nil {
		it.Aliases = make(map[string]string)
	}
	if len(args) == 0 {
		keys := make([]string, 0, len(it.Aliases))
		for k := range it.Aliases {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lines := make([]string, 0, len(keys))
		for _, k := range keys {
			lines = append(lines, fmt.Sprintf("alias %s='%s'", k, it.Aliases[k]))
		}
		it.LastStatus = 0
		return strings.Join(lines, "\n") + "\n", "", nil
	}
	for _, a := range args {
		if eq := strings.IndexByte(a, '='); eq > 0 {
			it.Aliases[a[:eq]] = a[eq+1:]
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

func (it *Interp) biUnalias(args []string) (string, string, error) {
	for _, a := range args {
		delete(it.Aliases, a)
	}
	it.LastStatus = 0
	return "", "", nil
}

func (it *Interp) biHistory(args []string) (string, string, error) {
	var b strings.Builder
	for i, cmd := range it.History {
		fmt.Fprintf(&b, "%5d  %s\n", i+1, cmd)
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

// biEnv 无参数时打印变量表；有 VAR=val 前缀时临时设置后执行后续命令。
func (it *Interp) biEnv(args []string, stdin string) (string, string, error) {
	i := 0
	for i < len(args) && isAssignmentWord(args[i]) {
		i++
	}
	if i == len(args) {
		keys := make([]string, 0, len(it.Vars))
		for k := range it.Vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&b, "%s=%s\n", k, it.Vars[k])
		}
		it.LastStatus = 0
		return b.String(), "", nil
	}

	type saved struct {
		val string
		set bool
	}
	saves := make(map[string]saved)
	for _, a := range args[:i] {
		eq := strings.IndexByte(a, '=')
		k, v := a[:eq], a[eq+1:]
		old, ok := it.Vars[k]
		saves[k] = saved{old, ok}
		it.Vars[k] = v
	}
	defer func() {
		for k, s := range saves {
			if s.set {
				it.Vars[k] = s.val
			} else {
				delete(it.Vars, k)
			}
		}
	}()

	// 参数已展开，按字面词构造命令执行。
	words := make([]*Word, 0, len(args)-i)
	for _, a := range args[i:] {
		words = append(words, &Word{Parts: []WordPart{&LitPart{Text: a}}})
	}
	out, _, err := it.execStmt(&Stmt{Cmd: &CallExpr{Args: words}}, stdin)
	return out, "", err
}

// biWhich 查找命令位置：内建/函数/别名回显名字，外部命令回显 PATH 中的路径。
func (it *Interp) biWhich(name string, args []string) (string, string, error) {
	names := args
	if name == "command" && len(args) > 0 {
		if args[0] == "-v" || args[0] == "-p" {
			names = args[1:]
		}
	}
	var out []string
	status := 0
	for _, n := range names {
		if isBuiltin(n) || it.Funcs[n] != nil || (it.Aliases != nil && it.Aliases[n] != "") {
			out = append(out, n)
			continue
		}
		if p, err := exec.LookPath(n); err == nil {
			out = append(out, p)
		} else {
			status = 1
		}
	}
	it.LastStatus = status
	if len(out) == 0 {
		return "", "", nil
	}
	return strings.Join(out, "\n") + "\n", "", nil
}
