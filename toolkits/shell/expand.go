package shell

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandCtx 是展开期上下文，由执行器注入。
// 命令替换与变量查询都通过函数字段注入，本包不直接访问进程或全局状态。
type ExpandCtx struct {
	// Vars 返回变量名的值与是否存在。
	Vars func(name string) (string, bool)
	// LastStatus 返回上一条命令的退出码（对应 $?）。
	LastStatus int
	// RunSubst 执行命令替换体，返回其标准输出（尾部换行由调用方裁剪）。
	// 为 nil 时命令替换原样保留为文本（安全降级）。
	RunSubst func(f *File) (string, error)
	// Cwd 用于 glob 相对路径解析。
	Cwd string
}

// field 是展开后的一个字段。globOK 表示该字段含未加引号的 glob 元字符，
// 可参与路径名展开（mvdan 的 quoteLevel 思想的极简版）。
type field struct {
	text   string
	globOK bool
}

// expandWord 把一个 Word 展开为若干字段。顺序遵循铁律：
// 参数/命令替换 → 分词 → glob → 去引号。
//
// 实现要点：先把每个 part 展开成带"是否可 glob"标记的片段；裸文本按 IFS
// （简化为空白）分词，引号内的片段不分词也不 glob，并与相邻裸片段粘连成
// 同一个字段（这是 "foo"$x"bar" 拼成一个参数的关键）。
func expandWord(w *Word, ctx ExpandCtx) ([]string, error) {
	type seg struct {
		text   string
		globOK bool // 来自未加引号的裸文本，含 *?[ 时可 glob
		quoted bool // 来自引号，强制不分词、不 glob
	}
	var segs []seg

	for _, part := range w.Parts {
		switch p := part.(type) {
		case *LitPart:
			segs = append(segs, seg{text: p.Text, globOK: true})
		case *SglQuotedPart:
			segs = append(segs, seg{text: p.Text, quoted: true})
		case *DblQuotedPart:
			var b strings.Builder
			for _, inner := range p.Parts {
				s, err := expandPartText(inner, ctx)
				if err != nil {
					return nil, err
				}
				b.WriteString(s)
			}
			segs = append(segs, seg{text: b.String(), quoted: true})
		case *ParamPart, *CmdSubstPart:
			s, err := expandPartText(p, ctx)
			if err != nil {
				return nil, err
			}
			// 未加引号的展开结果按 IFS 分词；展开产生的 * 不参与 glob（
			// 变量值里的通配符不应被当元字符，符合 bash 语义）。
			segs = append(segs, seg{text: s, globOK: false})
		}
	}

	// 构造字段：quoted 片段与相邻裸片段粘连；裸片段按空白切分并开新字段。
	var fields []field
	cur := ""
	curGlob := false
	hasCur := false
	flush := func() {
		if hasCur {
			fields = append(fields, field{text: cur, globOK: curGlob})
			cur, curGlob, hasCur = "", false, false
		}
	}
	for _, sg := range segs {
		if sg.quoted {
			cur += sg.text
			hasCur = true
			continue
		}
		// 裸片段：逐字符处理空白与 glob 标记。
		for _, piece := range splitKeepGlob(sg.text, &curGlob) {
			if piece == " " { // IFS 空白 → 结束当前字段
				flush()
				continue
			}
			cur += piece
			hasCur = true
		}
	}
	flush()

	// glob 展开：仅 globOK 且含元字符的字段参与。
	var out []string
	for _, f := range fields {
		if f.globOK && strings.ContainsAny(f.text, "*?[") {
			matches := globExpand(f.text, ctx.Cwd)
			if len(matches) > 0 {
				out = append(out, matches...)
				continue
			}
		}
		out = append(out, f.text)
	}
	return out, nil
}

// splitKeepGlob 把裸文本切成 token：空白作为 " " 单列，其余尽量整段保留。
// 它同时设置 globFound（该文本是否含 glob 元字符）。
func splitKeepGlob(s string, globFound *bool) []string {
	if strings.ContainsAny(s, "*?[") {
		*globFound = true
	}
	var out []string
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
			out = append(out, " ")
		} else {
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// expandPartText 展开单个 part 为文本（不分词、不 glob）。
func expandPartText(part WordPart, ctx ExpandCtx) (string, error) {
	switch p := part.(type) {
	case *LitPart:
		return p.Text, nil
	case *SglQuotedPart:
		return p.Text, nil
	case *DblQuotedPart:
		var b strings.Builder
		for _, inner := range p.Parts {
			s, err := expandPartText(inner, ctx)
			if err != nil {
				return "", err
			}
			b.WriteString(s)
		}
		return b.String(), nil
	case *ParamPart:
		switch p.Kind {
		case ParamLastStatus:
			return itoa(ctx.LastStatus), nil
		case ParamVar, ParamVarDefault:
			v, ok := ctx.Vars(p.Name)
			if !ok || (p.Kind == ParamVarDefault && v == "") {
				return p.Default, nil
			}
			return v, nil
		}
	case *CmdSubstPart:
		if ctx.RunSubst == nil {
			return p.Raw, nil // 安全降级：原样保留
		}
		out, err := ctx.RunSubst(p.Body)
		if err != nil {
			return "", err
		}
		// POSIX：裁掉尾部所有换行，剔除 NUL。
		out = strings.TrimRight(out, "\n")
		out = strings.ReplaceAll(out, "\x00", "")
		return out, nil
	}
	return "", nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// globExpand 在 cwd 下展开 glob 模式，返回排序后的匹配路径（相对名）。
func globExpand(pattern, cwd string) []string {
	if cwd == "" {
		cwd = "."
	}
	dir, base := filepath.Split(pattern)
	if dir == "" {
		dir = "."
	}
	absDir := dir
	if !filepath.IsAbs(dir) {
		absDir = filepath.Join(cwd, dir)
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		matched, err := filepath.Match(base, e.Name())
		if err != nil || !matched {
			continue
		}
		if dir == "." || dir == "" {
			out = append(out, e.Name())
		} else {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}
