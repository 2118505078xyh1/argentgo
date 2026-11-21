package shell

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// 本文件守护词法/语法/执行三层的既有不变量，不触碰生产逻辑，也不依赖
// 其它守卫测试文件。所有新辅助均以 guard 前缀命名，避免与同包测试冲突。

// guardLex 词法化 src，失败即 Fatal；返回含末尾 TokEOF 的 token 序列。
func guardLex(t *testing.T, src string) []Token {
	t.Helper()
	toks, err := newLexer(src).lex()
	if err != nil {
		t.Fatalf("lex(%q) 意外失败: %v", src, err)
	}
	return toks
}

// guardKinds 抽取 token 种类序列（含 EOF），便于整段比对。
func guardKinds(toks []Token) []TokenKind {
	kinds := make([]TokenKind, 0, len(toks))
	for _, tk := range toks {
		kinds = append(kinds, tk.Kind)
	}
	return kinds
}

// guardAllRaws 按出现顺序返回全部 token 的原文（含操作符，忽略末尾空 EOF）。
func guardAllRaws(toks []Token) []string {
	raws := make([]string, 0, len(toks))
	for _, tk := range toks {
		if tk.Kind == TokEOF {
			continue
		}
		raws = append(raws, tk.Raw)
	}
	return raws
}

// guardWordRaws 只返回 TokWord 的原文。
func guardWordRaws(toks []Token) []string {
	var raws []string
	for _, tk := range toks {
		if tk.Kind == TokWord {
			raws = append(raws, tk.Raw)
		}
	}
	return raws
}

// guardAssertBytes 断言 raw 与 want 逐字节相等且是合法 UTF-8。
func guardAssertBytes(t *testing.T, raw, want string) {
	t.Helper()
	if !utf8.ValidString(raw) {
		t.Errorf("原文 %q 不是合法 UTF-8", raw)
	}
	if !reflect.DeepEqual([]byte(raw), []byte(want)) {
		t.Errorf("原文逐字节不等: got %q (%x), want %q (%x)", raw, []byte(raw), want, []byte(want))
	}
}

// guardRun 在独立临时工作区执行 cmd。
func guardRun(t *testing.T, cmd string) (Result, error) {
	t.Helper()
	return Run(cmd, testOptions(t.TempDir()))
}

// guardRejects 断言 cmd 被 fail-closed 拒绝：Supported 为 false，或 Run
// 返回 ErrUnsupported。两个方向都检查，避免"静态拒绝但运行时放行"的漏洞。
func guardRejects(t *testing.T, cmd string) {
	t.Helper()
	_, err := guardRun(t, cmd)
	switch {
	case !Supported(cmd):
		if err == nil {
			t.Errorf("%q: Supported=false 但 Run 成功，fail-closed 被绕过", cmd)
		}
	case errors.Is(err, ErrUnsupported):
		// 运行期明确拒绝，符合 fail-closed。
	default:
		t.Errorf("%q: Supported=true 且 Run 未返回 ErrUnsupported (err=%v)", cmd, err)
	}
}

// guardExpandCtx 提供确定性的展开上下文，用于验证展开期不变量。
func guardExpandCtx() ExpandCtx {
	return ExpandCtx{
		Vars: func(name string) (string, bool) {
			if name == "FOO" {
				return "bar", true
			}
			return "", false
		},
		LastStatus: 7,
		RunSubst:   func(*File) (string, error) { return "sub\n", nil },
	}
}

// TestGuardLexerQuoteEscapeRawStable 守护引号与反斜杠转义：lexWord 必须把
// 引号/转义符连同内容逐字节原样收进 TokWord.Raw，去引号留给展开期。
func TestGuardLexerQuoteEscapeRawStable(t *testing.T) {
	cases := []struct {
		src  string
		want []string
	}{
		{`echo 'hello world'`, []string{"echo", "'hello world'"}},
		{`echo "hello world"`, []string{"echo", `"hello world"`}},
		{`echo a\ b`, []string{"echo", `a\ b`}},
		{`echo 'a"b' "c'd"`, []string{"echo", `'a"b'`, `"c'd"`}},
		{`echo a\"b\ c`, []string{"echo", `a\"b\ c`}},
		{`echo '' ""`, []string{"echo", "''", `""`}},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			toks := guardLex(t, tc.src)
			got := guardWordRaws(toks)
			if len(got) != len(tc.want) {
				t.Fatalf("word 数量: got %d, want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				guardAssertBytes(t, got[i], tc.want[i])
			}
			// token 序列必须是「N 个 word + EOF」，无杂散操作符。
			wantKinds := make([]TokenKind, 0, len(tc.want)+1)
			for range tc.want {
				wantKinds = append(wantKinds, TokWord)
			}
			wantKinds = append(wantKinds, TokEOF)
			if !reflect.DeepEqual(guardKinds(toks), wantKinds) {
				t.Fatalf("token 种类: got %v, want %v", guardKinds(toks), wantKinds)
			}
			// 本组样例词间都是单个空格，raw 以单空格拼接应精确还原源码。
			if joined := strings.Join(got, " "); joined != tc.src {
				t.Fatalf("逐字节回放: got %q, want %q", joined, tc.src)
			}
		})
	}
}

// TestGuardLexerUnclosedQuotesFail 守护未闭合引号：必须在词法阶段报错，
// 绝不能静默产出"差不多"的单词。
func TestGuardLexerUnclosedQuotesFail(t *testing.T) {
	for _, src := range []string{`echo 'abc`, `echo "abc`, `echo 'a"b`, `echo "a'b`} {
		if _, err := newLexer(src).lex(); err == nil {
			t.Errorf("%q: 未闭合引号应报 LexError", src)
		}
	}
}

// TestGuardLexerOperatorSplit 守护 ; && || | 的分流：lexer 产出正确 token，
// parser 建出正确的二元操作树（左结合，四种 op 都出现且不串味）。
func TestGuardLexerOperatorSplit(t *testing.T) {
	cases := []struct {
		src       string
		wantKinds []TokenKind
	}{
		{`echo a && echo b`, []TokenKind{TokWord, TokWord, TokAnd, TokWord, TokWord, TokEOF}},
		{`echo a || echo b`, []TokenKind{TokWord, TokWord, TokOr, TokWord, TokWord, TokEOF}},
		{`echo a | cat`, []TokenKind{TokWord, TokWord, TokPipe, TokWord, TokEOF}},
		{`echo a ; true`, []TokenKind{TokWord, TokWord, TokSemi, TokWord, TokEOF}},
		{
			`echo a && echo b || echo c | cat ; true`,
			[]TokenKind{
				TokWord, TokWord, TokAnd, TokWord, TokWord, TokOr,
				TokWord, TokWord, TokPipe, TokWord, TokSemi, TokWord, TokEOF,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			toks := guardLex(t, tc.src)
			if !reflect.DeepEqual(guardKinds(toks), tc.wantKinds) {
				t.Fatalf("token 种类: got %v, want %v", guardKinds(toks), tc.wantKinds)
			}
			if _, err := parse(toks); err != nil {
				t.Fatalf("parse(%q): %v", tc.src, err)
			}
		})
	}

	// 四种 op 在同一条命令里各就各位：; 在最外层，|| 包住 |，&& 在最内。
	f, err := parse(guardLex(t, `echo a && echo b || echo c | cat ; true`))
	if err != nil {
		t.Fatal(err)
	}
	seq, ok := f.Stmt.Cmd.(*BinaryCmd)
	if !ok || seq.Op != OpSeq {
		t.Fatalf("最外层应为 OpSeq, got %#v", f.Stmt.Cmd)
	}
	or, ok := seq.X.Cmd.(*BinaryCmd)
	if !ok || or.Op != OpOr {
		t.Fatalf("左子树应为 OpOr, got %#v", seq.X.Cmd)
	}
	and, ok := or.X.Cmd.(*BinaryCmd)
	if !ok || and.Op != OpAnd {
		t.Fatalf("OpOr 左子树应为 OpAnd, got %#v", or.X.Cmd)
	}
	pipe, ok := or.Y.Cmd.(*BinaryCmd)
	if !ok || pipe.Op != OpPipe {
		t.Fatalf("OpOr 右子树应为 OpPipe, got %#v", or.Y.Cmd)
	}
}

// TestGuardLexerRedirectsStable 守护 > >> < 2>&1 的 token 原文与 AST 结构，
// 特别是相邻重定向不能被吞并或错位。
func TestGuardLexerRedirectsStable(t *testing.T) {
	cases := []struct {
		src       string
		wantKinds []TokenKind
		wantRaws  []string
	}{
		{`echo hi > out`, []TokenKind{TokWord, TokWord, TokRedirOut, TokWord, TokEOF}, []string{"echo", "hi", ">", "out"}},
		{`echo hi >> out`, []TokenKind{TokWord, TokWord, TokRedirAppend, TokWord, TokEOF}, []string{"echo", "hi", ">>", "out"}},
		{`cat < in`, []TokenKind{TokWord, TokRedirIn, TokWord, TokEOF}, []string{"cat", "<", "in"}},
		{`echo hi 2>&1`, []TokenKind{TokWord, TokWord, TokRedirDupOut, TokEOF}, []string{"echo", "hi", "2>&1"}},
		{
			`echo hi > out 2>&1 >> app < in`,
			[]TokenKind{
				TokWord, TokWord, TokRedirOut, TokWord, TokRedirDupOut,
				TokRedirAppend, TokWord, TokRedirIn, TokWord, TokEOF,
			},
			[]string{"echo", "hi", ">", "out", "2>&1", ">>", "app", "<", "in"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			toks := guardLex(t, tc.src)
			if !reflect.DeepEqual(guardKinds(toks), tc.wantKinds) {
				t.Fatalf("token 种类: got %v, want %v", guardKinds(toks), tc.wantKinds)
			}
			raws := guardAllRaws(toks)
			if len(raws) != len(tc.wantRaws) {
				t.Fatalf("raw 数量: got %d, want %d", len(raws), len(tc.wantRaws))
			}
			for i := range tc.wantRaws {
				guardAssertBytes(t, raws[i], tc.wantRaws[i])
			}
			if _, err := parse(toks); err != nil {
				t.Fatalf("parse(%q): %v", tc.src, err)
			}
		})
	}

	f, err := parse(guardLex(t, `echo hi > out 2>&1 >> app < in`))
	if err != nil {
		t.Fatal(err)
	}
	wantOps := []RedirOp{RedirOut, RedirDupOut, RedirAppend, RedirIn}
	wantN := []int{1, 2, 1, 0}
	if len(f.Stmt.Redirs) != len(wantOps) {
		t.Fatalf("重定向数量: got %d, want %d", len(f.Stmt.Redirs), len(wantOps))
	}
	for i, r := range f.Stmt.Redirs {
		if r.Op != wantOps[i] || r.N != wantN[i] {
			t.Errorf("重定向[%d]: got op=%v n=%d, want op=%v n=%d", i, r.Op, r.N, wantOps[i], wantN[i])
		}
	}
}

// TestGuardLexerExpansionsKeptRaw 守护 $VAR / $? / ${VAR:-def} / $(cmd)
// 在词法层原样保留（含嵌套 $(...) 的括号深度平衡），不得提前展开。
func TestGuardLexerExpansionsKeptRaw(t *testing.T) {
	cases := []struct {
		src  string
		want []string
	}{
		{`echo $FOO`, []string{"echo", "$FOO"}},
		{`echo $?`, []string{"echo", "$?"}},
		{`echo ${VAR:-def}`, []string{"echo", "${VAR:-def}"}},
		{`echo $(cmd)`, []string{"echo", "$(cmd)"}},
		{`echo pre$FOO-post`, []string{"echo", "pre$FOO-post"}},
		{`echo $(echo $(echo hi))`, []string{"echo", "$(echo $(echo hi))"}},
		{`echo "$FOO" '$FOO'`, []string{"echo", `"$FOO"`, `'$FOO'`}},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			toks := guardLex(t, tc.src)
			got := guardWordRaws(toks)
			if len(got) != len(tc.want) {
				t.Fatalf("word 数量: got %d, want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				guardAssertBytes(t, got[i], tc.want[i])
			}
		})
	}
}

// TestGuardExpansionPartsAndValues 守护展开期不变量：part 种类正确，展开值
// 正确，单引号不展开、双引号展开但不分词、命令替换由注入的 RunSubst 执行。
func TestGuardExpansionPartsAndValues(t *testing.T) {
	src := `echo $FOO $? ${VAR:-def} $(echo hi) "$FOO" '$FOO'`
	f, err := parse(guardLex(t, src))
	if err != nil {
		t.Fatal(err)
	}
	call, ok := f.Stmt.Cmd.(*CallExpr)
	if !ok {
		t.Fatalf("顶层应为 CallExpr, got %#v", f.Stmt.Cmd)
	}
	wantFields := [][]string{
		{"echo"}, {"bar"}, {"7"}, {"def"}, {"sub"}, {"bar"}, {"$FOO"},
	}
	if len(call.Args) != len(wantFields) {
		t.Fatalf("word 数量: got %d, want %d", len(call.Args), len(wantFields))
	}
	for i, w := range call.Args {
		fields, err := expandWord(w, guardExpandCtx())
		if err != nil {
			t.Fatalf("expandWord(word[%d]): %v", i, err)
		}
		if !reflect.DeepEqual(fields, wantFields[i]) {
			t.Errorf("word[%d] 展开: got %q, want %q", i, fields, wantFields[i])
		}
	}

	// part 结构：$FOO / $? / ${VAR:-def} / $(cmd) / "$FOO" / '$FOO'。
	p1, ok := call.Args[1].Parts[0].(*ParamPart)
	if !ok || p1.Kind != ParamVar || p1.Name != "FOO" {
		t.Errorf("$FOO part: got %#v", call.Args[1].Parts[0])
	}
	p2, ok := call.Args[2].Parts[0].(*ParamPart)
	if !ok || p2.Kind != ParamLastStatus {
		t.Errorf("$? part: got %#v", call.Args[2].Parts[0])
	}
	p3, ok := call.Args[3].Parts[0].(*ParamPart)
	if !ok || p3.Kind != ParamVarDefault || p3.Name != "VAR" || p3.Default != "def" {
		t.Errorf("${VAR:-def} part: got %#v", call.Args[3].Parts[0])
	}
	p4, ok := call.Args[4].Parts[0].(*CmdSubstPart)
	if !ok || p4.Body == nil || p4.Raw != "echo hi" {
		t.Errorf("$(cmd) part: got %#v", call.Args[4].Parts[0])
	}
	p5, ok := call.Args[5].Parts[0].(*DblQuotedPart)
	if !ok || len(p5.Parts) != 1 {
		t.Errorf(`"$FOO" part: got %#v`, call.Args[5].Parts[0])
	}
	p6, ok := call.Args[6].Parts[0].(*SglQuotedPart)
	if !ok || p6.Text != "$FOO" {
		t.Errorf(`'$FOO' part: got %#v`, call.Args[6].Parts[0])
	}
}

// TestGuardLexerGlobBytesStable 守护 glob 模式在词法层逐字节稳定，且通配符
// 不会把相邻的多字节路径名切断；执行期 glob 命中文件名也保持 UTF-8。
func TestGuardLexerGlobBytesStable(t *testing.T) {
	patterns := []string{`*.go`, `a?[bc]`, `**/*`, `文件*.txt`, `src/[a-z]*.go`}
	for _, pat := range patterns {
		t.Run(pat, func(t *testing.T) {
			src := "echo " + pat
			toks := guardLex(t, src)
			got := guardWordRaws(toks)
			if len(got) != 2 {
				t.Fatalf("word 数量: got %d, want 2 (%v)", len(got), got)
			}
			guardAssertBytes(t, got[0], "echo")
			guardAssertBytes(t, got[1], pat)
			// 确定性：同一输入两次词法化必须完全一致（含 Pos）。
			if again := guardLex(t, src); !reflect.DeepEqual(toks, again) {
				t.Fatalf("两次 lex 结果不一致: %v vs %v", toks, again)
			}
		})
	}

	// 执行期：CJK 文件名经 glob 命中后逐字节原样输出。
	dir := t.TempDir()
	if err := guardWriteFile(dir, "中文.txt", "x"); err != nil {
		t.Fatal(err)
	}
	got, err := Run("echo *.txt", testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != "中文.txt\n" {
		t.Fatalf("glob CJK 文件: got %q, want %q", got.Output, "中文.txt\n")
	}
}

// guardWriteFile 在 dir 下写一个文件，返回错误供测试判定。
func guardWriteFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

// TestGuardFailClosedConstructs 守护 fail-closed：循环/函数/case/子 shell/
// 算术/后台等构造 100% 被拒绝（Supported=false 或 Run 返回 ErrUnsupported）。
func TestGuardFailClosedConstructs(t *testing.T) {
	cases := []string{
		"for i in a; do echo $i; done",
		"while true; do echo x; done",
		"until false; do echo x; done",
		"if true; then echo x; fi",
		"case x in a) echo a ;; esac",
		"function f { echo x; }",
		"select x in a; do echo x; done",
		"echo hi &",
		"( echo hi )",
		"( echo ( nested ) hi )",
		"echo $((1+2))",
		"echo $(( 1 + 2 ))",
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			guardRejects(t, cmd)
		})
	}
}

// TestGuardLexerUTF8Integrity 守护 CJK/emoji/控制字符的 UTF-8 完整性：
// lexWord 按 rune 拼回原文，绝不在多字节序列中间截断；控制字符若非空白
// 则作为普通字符保留原文。
func TestGuardLexerUTF8Integrity(t *testing.T) {
	cases := []struct {
		src  string
		want []string
	}{
		{"echo 你好世界", []string{"echo", "你好世界"}},
		{"echo \U0001F642\U0001F680", []string{"echo", "\U0001F642\U0001F680"}},
		{"echo 中文\U0001F642mixed", []string{"echo", "中文\U0001F642mixed"}},
		{"echo \U0001F468\u200d\U0001F469\u200d\U0001F467", []string{"echo", "\U0001F468\u200d\U0001F469\u200d\U0001F467"}},
		{`echo "你好🙂"`, []string{"echo", `"你好🙂"`}},
		{"echo 中文\\ 词组", []string{"echo", `中文\ 词组`}},
		{"echo a\x01b", []string{"echo", "a\x01b"}},
		{`echo "a\x7fb"`, []string{"echo", `"a\x7fb"`}},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			toks := guardLex(t, tc.src)
			got := guardWordRaws(toks)
			if len(got) != len(tc.want) {
				t.Fatalf("word 数量: got %d, want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				guardAssertBytes(t, got[i], tc.want[i])
			}
		})
	}
}

// TestGuardNoInputMutationAndDeterminism 守护纯函数性：lex/parse 不修改入参，
// Run 不修改传入的变量表；同一输入重复执行得到确定一致的结果。
func TestGuardNoInputMutationAndDeterminism(t *testing.T) {
	src := "echo '你好🙂 $FOO' a\\ b $(echo hi) > out 2>&1 ; true"
	orig := src

	toks1 := guardLex(t, src)
	if src != orig {
		t.Fatalf("lex 修改了入参: got %q, want %q", src, orig)
	}
	toks2 := guardLex(t, src)
	if !reflect.DeepEqual(toks1, toks2) {
		t.Fatalf("lex 非确定性: %v vs %v", toks1, toks2)
	}

	// parse 不得改动传入的 token 切片（含 Pos）。
	saved := append([]Token(nil), toks1...)
	if _, err := parse(toks1); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(toks1, saved) {
		t.Fatalf("parse 修改了 token 切片: %v vs %v", toks1, saved)
	}

	// Run 不得改动 opts.Vars（copyVars 隔离），且重复执行结果一致。
	opts := testOptions(t.TempDir())
	varsBefore := make(map[string]string, len(opts.Vars))
	for k, v := range opts.Vars {
		varsBefore[k] = v
	}
	cmd := "echo $FOO ${MISSING:-d}"
	res1, err1 := Run(cmd, opts)
	res2, err2 := Run(cmd, opts)
	if err1 != nil || err2 != nil {
		t.Fatalf("Run 意外失败: %v / %v", err1, err2)
	}
	if !reflect.DeepEqual(res1, res2) {
		t.Fatalf("Run 非确定性: %#v vs %#v", res1, res2)
	}
	if !reflect.DeepEqual(opts.Vars, varsBefore) {
		t.Fatalf("Run 修改了入参变量表: got %v, want %v", opts.Vars, varsBefore)
	}
}
