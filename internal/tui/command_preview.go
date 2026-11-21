package tui

import "strings"

// 自研 shell 命令预览的三个渲染原语。它们只生成稳定的单行文本，不接任何
// 渲染器、不读取 Model/时钟，也不引用 security，全部是可在包内独立测试的
// 纯函数。当前没有任何调用方；留作 dock / 审批行日后展示命令摘要的基建。
//
// 设计上与 approval_projection.go 的 ApprovalLine 同构（同样的光标 + 危险
// 标记 + 主体 + " — " 原因分隔），但这里的对象是命令文本而非工具调用。

const (
	// defaultPreviewCells 是 PreviewCommand 在 maxCells <= 0 时采用的上限。
	// 60 与既有工具参数摘要的截断宽度同量级：足够显示一条常见命令，
	// 又不至于撑破 dock 的单行布局。
	defaultPreviewCells = 60

	// FormatCommandLine 的固定文案。集中为常量，便于日后整批迁往
	// localeStrings，无需改动 FormatCommandLine 的控制流。
	commandLineCursor     = "$ "  // 命令行前缀（光标/提示符）
	commandLineDangerMark = "⚠ "  // 危险项标记，与 ApprovalLine 的标记同形
	commandLineReasonSep  = " — " // 原因与主体之间的分隔符
)

// PreviewCommand 把命令折叠成可安全单行显示的预览文本。
//
// 处理顺序固定为：先用 collapseSpace 把所有空白（含换行、制表符）折叠为
// 单个空格，保证结果是单行；再把剩余的 C0 控制字符与 DEL 转义成可见的
// "\xNN" 形式（小写十六进制，定宽且稳定）；最后按显示单元格
// 有界截断，超出时以省略号 "…" 收尾。截断复用 Truncate，因此切口落在
// 字素簇边界：CJK 汉字、emoji、ZWJ 组合与旗帜都不会被切坏，也绝不会在
// 多字节 UTF-8 序列中间断开。
//
// maxCells <= 0 时改用 defaultPreviewCells。纯函数：不修改 command，
// 同一输入永远得到同一输出。
func PreviewCommand(command string, maxCells int) string {
	if maxCells <= 0 {
		maxCells = defaultPreviewCells
	}
	flat := collapseSpace(command)
	escaped := escapeControlCells(flat)
	return Truncate(escaped, maxCells)
}

// escapeControlCells 把 s 中残留的不可打印 ASCII 控制字符（< 0x20 或
// 0x7f）改写为稳定的 "\xNN" 四字符序列。逐字节复制即可，因为
// 控制字符都是单字节 ASCII，而 UTF-8 的后续字节均 >= 0x80，按字节拷贝
// 不会破坏多字节字符。制表符/换行等空白已由 collapseSpace 折叠，
// 通常不会走到这里；此函数兜住 ESC、NUL、BEL、DEL 等残留控制符。
func escapeControlCells(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f {
			const hex = "0123456789abcdef"
			b.WriteString(`\x`)
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// commandGlyphTokens 是预览关心的危险符号，按"最长优先"排列，使 >> 先于
// >、|| 先于 |、2>&1 作为一个整体被识别。
var commandGlyphTokens = []string{
	"2>&1", // 输出合并重定向
	"&&",   // 逻辑与
	"||",   // 逻辑或
	">>",   // 追加重定向
	"$(",   // 命令替换
	";",    // 命令分隔
	"|",    // 管道
	">",    // 重定向
}

// CommandGlyphs 提取命令中出现的危险符号序列，返回以单个空格连接的稳定
// 标记串。扫描按从左到右、按出现顺序记录每一次匹配，重复出现也照实保留，
// 因此结果就是命令里危险符号的"序列"；没有任何危险符号时返回空串。
//
// 这是纯文本启发式：不做 shell 语法解析，引号内的分号也会被记入，
// 目的是渲染摘要而非做安全判定（安全判定由 security 包负责，tui 不介入）。
// 最长匹配优先保证 "||" 不会被拆成两个 "|"，"2>&1" 不会被拆成 "2" 与 ">"。
func CommandGlyphs(command string) string {
	var found []string
	for i := 0; i < len(command); {
		matched := ""
		for _, token := range commandGlyphTokens {
			if strings.HasPrefix(command[i:], token) {
				matched = token
				break
			}
		}
		if matched != "" {
			found = append(found, matched)
			i += len(matched)
			continue
		}
		i++
	}
	return strings.Join(found, " ")
}

// FormatCommandLine 把命令与原因组合成一条稳定的单行文本，供逐行显示：
//
//	<光标><危险标记><命令预览><分隔符><原因>
//
// 命令部分通过 PreviewCommand 生成（因此同样折叠空白、转义控制字符并按
// 默认上限截断），原因部分折叠空白后复用 Truncate 截断到同一上限。
// danger 只决定是否附加危险标记；不传 selected，光标恒为固定前缀。
// 纯函数：不修改入参，同一输入永远得到同一输出。
func FormatCommandLine(command, reason string, danger bool) string {
	var b strings.Builder
	b.WriteString(commandLineCursor)
	if danger {
		b.WriteString(commandLineDangerMark)
	}
	b.WriteString(PreviewCommand(command, defaultPreviewCells))
	if flat := collapseSpace(reason); flat != "" {
		b.WriteString(commandLineReasonSep)
		b.WriteString(Truncate(flat, defaultPreviewCells))
	}
	return b.String()
}
