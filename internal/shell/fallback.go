package shell

import "errors"

// CompatEntry 是兼容矩阵中的一行，描述一类语法在本子集中的处理方式。
type CompatEntry struct {
	Syntax string // 语法类别
	Native bool   // true=解析期可判定且执行期能正确解释；false=必须整条回退系统 shell
	Note   string // 判定依据与回退原因
}

// compatibilityMatrix 是"兼容矩阵"的显性声明，供审计与测试引用。
//
// 判定策略：所有 Native=false 的构造都在解析期由 containsUnsupported 递归识别，
// 使 Supported() 在整条命令执行前就返回 false——即"整条命令级回退"，杜绝部分
// 执行后再回退造成重定向/写入等副作用重复。执行期若仍冒出 ErrUnsupported
// （如命令替换动态产出保留字当程序名这类无法静态判定的情形），ShouldFallback
// 让调用方用原始命令行整条回退。
var compatibilityMatrix = []CompatEntry{
	{"简单命令与参数展开", true, "CallExpr：$VAR/$?/${VAR:-def}/$(cmd)/引号/glob 均在执行期展开"},
	{"逻辑与、或、管道、顺序", true, "BinaryCmd：&& || | ; 的 X、Y 两侧均递归检查"},
	{"输入输出重定向", true, "Redirect：> >> < 2>&1，目标单词内的命令替换同样递归检查"},
	{"内建与外部命令", true, "内建表 + exec.LookPath；非零退出用 ExitStatus 表达"},
	{"循环/条件/函数等保留字", false, "for/while/if/function... 解析期打标或执行期 ErrUnsupported"},
	{"子 shell ( ... ) 与后台任务 &", false, "词法阶段 TokUnsupported → UnsupportedCmd"},
	{"行首注释 #", false, "TokUnsupported，交由系统 shell 给出正确语义"},
	{"算术 $(( )) 与未闭合构造", false, "词法/语法阶段报错，Supported() 直接 false"},
	{"命令替换体内含不支持构造", false, "递归遍历 CmdSubstPart.Body，整条回退"},
	{"动态产生的保留字程序名", false, "无法静态判定，执行期 ErrUnsupported → ShouldFallback"},
}

// CompatibilityMatrix 返回兼容矩阵的副本，供审计与文档生成使用。
func CompatibilityMatrix() []CompatEntry {
	out := make([]CompatEntry, len(compatibilityMatrix))
	copy(out, compatibilityMatrix)
	return out
}

// ShouldFallback 报告一个错误是否表示"本子集无法正确执行，须整条命令回退系统 shell"。
//
// 它同时识别哨兵 ErrUnsupported 与携带构造名的 *UnsupportedError（经 Unwrap），
// 以及用 %w 包装它们的错误。调用方一旦得到 true，必须用**原始命令行**重跑整条命令，
// 绝不在部分执行后继续或重试片段——否则重定向/写入等副作用会重复。
//
// 这是解析期 Supported() 预检之外的运行时安全网：绝大多数不支持构造在解析期就被
// containsUnsupported 拦截；只有动态行为（如命令替换产出保留字当程序名）才会走到这里。
func ShouldFallback(err error) bool {
	return errors.Is(err, ErrUnsupported)
}

// containsUnsupported 递归判断一条语句（及其所有嵌套节点）是否含本子集不支持的
// 构造。返回 true 表示整条命令必须回退系统 shell。
//
// 递归覆盖范围：
//   - UnsupportedCmd：词法/语法阶段打标的构造（后台 &、子 shell、注释等）；
//   - BinaryCmd：&& || | ; 的 X、Y 两侧；
//   - CallExpr：所有参数单词（不止第一个）里藏着的命令替换；程序名为保留字时判不支持；
//   - Word / WordPart：DblQuotedPart 内的嵌套 part、CmdSubstPart.Body（递归到内部 File.Stmt）；
//   - Redirect：Op==0 的畸形重定向，以及目标单词里嵌套的命令替换。
func containsUnsupported(st *Stmt) bool {
	if st == nil {
		return false
	}
	switch c := st.Cmd.(type) {
	case *UnsupportedCmd:
		return true
	case *BinaryCmd:
		return containsUnsupported(c.X) || containsUnsupported(c.Y)
	case *CallExpr:
		// 每个单词都可能藏有命令替换，逐个检查（含第一个单词）。
		for _, w := range c.Args {
			if wordContainsUnsupported(w) {
				return true
			}
		}
		// 保留字当程序名即不支持的控制结构（循环/函数/条件等）。
		if len(c.Args) > 0 && len(c.Args[0].Parts) > 0 {
			if lit, ok := c.Args[0].Parts[0].(*LitPart); ok && isReservedWord(lit.Text) {
				return true
			}
		}
	}
	// 重定向目标同样可能含命令替换；畸形重定向（Op==0）也不支持。
	for _, r := range st.Redirs {
		if r.Op == 0 || wordContainsUnsupported(r.Target) {
			return true
		}
	}
	return false
}

// wordContainsUnsupported 递归检查一个单词的所有 WordPart。
func wordContainsUnsupported(w *Word) bool {
	if w == nil {
		return false
	}
	return partsContainUnsupported(w.Parts)
}

// partsContainUnsupported 检查一组 WordPart，下钻命令替换体与双引号内的嵌套 part。
func partsContainUnsupported(parts []WordPart) bool {
	for _, part := range parts {
		switch p := part.(type) {
		case *CmdSubstPart:
			if fileContainsUnsupported(p.Body) {
				return true
			}
		case *DblQuotedPart:
			if partsContainUnsupported(p.Parts) {
				return true
			}
		}
	}
	return false
}

// fileContainsUnsupported 递归判定命令替换体（File）是否含不支持构造。
// 空 Body 无构造可执行，视为安全。
func fileContainsUnsupported(f *File) bool {
	if f == nil {
		return false
	}
	return containsUnsupported(f.Stmt)
}
