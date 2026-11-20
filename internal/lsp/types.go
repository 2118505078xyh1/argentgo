package lsp

import "fmt"

// Position 是 LSP 定义的文档中从零开始的位置。
type Position struct {
	// Line 从零开始。
	Line int `json:"line"`
	// Character 是行内从零开始的 UTF-16 码元偏移。
	Character int `json:"character"`
}

// String 按人类习惯的 1 基 行:列 渲染位置。
func (p Position) String() string {
	return fmt.Sprintf("%d:%d", p.Line+1, p.Character+1)
}

// Human 把 1 基的行和列转换为 Position。
func Human(line, column int) Position {
	if line < 1 {
		line = 1
	}
	if column < 1 {
		column = 1
	}
	return Position{Line: line - 1, Character: column - 1}
}

// Range 是两个位置之间的区间。
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Diagnostic 是服务器报告的一处文档问题。
type Diagnostic struct {
	URI      string `json:"uri"`
	Range    Range  `json:"range"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Source   string `json:"source,omitempty"`
}

// Location 是定义目标，从 Location 或 LocationLink 规范化而来。
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// Hover 是服务器在某位置提供的文档。
type Hover struct {
	Contents string
	Range    *Range
}

// TextEdit 是用新文本替换文档某区间的一次操作。
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// Symbol 是来自文档符号树或工作区符号列表的一个扁平条目。顶层条目的
// Symbol 是来自文档符号树或工作区符号列表的一个扁平条目。顶层条目的 Depth 为 0，随嵌套增长；它由客户端推导，因此不属于线上格式。
// 由客户端推导，因此不属于线上格式。
type Symbol struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	Range          Range  `json:"range"`
	SelectionRange Range  `json:"selectionRange"`
	URI            string `json:"uri,omitempty"`
	Detail         string `json:"detail,omitempty"`
	Depth          int    `json:"-"`
}

// SymbolInformation 是扁平 SymbolInformation 形态，由
// SymbolInformation 是 workspace/symbol 以及用它作为文档符号的服务器返回的扁平 SymbolInformation 形态。
type SymbolInformation struct {
	Name          string   `json:"name"`
	Kind          int      `json:"kind"`
	Location      Location `json:"location"`
	ContainerName string   `json:"containerName,omitempty"`
	Deprecated    bool     `json:"deprecated,omitempty"`
	Tags          []int    `json:"tags,omitempty"`
}

// DocumentEdit 是来自 WorkspaceEdit.documentChanges 数组的单个文件的文本编辑。资源操作（创建/重命名/删除）不带文本编辑，被跳过。
// 数组。资源操作（创建/重命名/删除）不带文本编辑且
// 被跳过。
type DocumentEdit struct {
	URI   string     `json:"uri"`
	Edits []TextEdit `json:"edits"`
}

// WorkspaceEdit 按文件对文本编辑分组。Changes 始终保存合并
// WorkspaceEdit 按文件对文本编辑分组。Changes 始终保存旧 changes map 与 documentChanges 的合并视图；DocumentChanges 保留服务器发送的原样按文件条目。
// 按文件条目保持服务器发送时的原样。
type WorkspaceEdit struct {
	Changes         map[string][]TextEdit `json:"changes,omitempty"`
	DocumentChanges []DocumentEdit        `json:"documentChanges,omitempty"`
}

// Command 描述代码动作携带的命令引用。
type Command struct {
	Title     string `json:"title"`
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

// CodeActionDisabled 解释某动作为何不可用。
type CodeActionDisabled struct {
	Reason string `json:"reason"`
}

// CodeAction 是服务器为某区间提供的一个修复或重构。
type CodeAction struct {
	Title       string              `json:"title"`
	Kind        string              `json:"kind,omitempty"`
	Diagnostics []Diagnostic        `json:"diagnostics,omitempty"`
	Edit        *WorkspaceEdit      `json:"edit,omitempty"`
	Command     *Command            `json:"command,omitempty"`
	IsPreferred bool                `json:"isPreferred,omitempty"`
	Disabled    *CodeActionDisabled `json:"disabled,omitempty"`
}

// FormattingOptions 是标准格式化选项对象。布尔指针
// FormattingOptions 是标准格式化选项对象。布尔指针用于区分"未设置"与显式 false。
type FormattingOptions struct {
	TabSize                int   `json:"tabSize"`
	InsertSpaces           bool  `json:"insertSpaces"`
	TrimTrailingWhitespace *bool `json:"trimTrailingWhitespace,omitempty"`
	InsertFinalNewline     *bool `json:"insertFinalNewline,omitempty"`
	TrimFinalNewlines      *bool `json:"trimFinalNewlines,omitempty"`
}

// SemanticTokens 是 textDocument/semanticTokens/full 请求的结果。
type SemanticTokens struct {
	// Data 是扁平的增量编码 token 流：每 token 五个 uint32 值
	// Data 是扁平的增量编码 token 流：每个 token 五个 uint32 值（deltaLine、deltaStart、length、tokenType、tokenModifiers）。客户端保留原始切片；把它解码成行是 UI 的事。
	// 客户端保留原始切片；把它解码成行是 UI 的事。
	Data []uint32 `json:"data"`
	// ResultID 是服务器的版本标签，用于 semanticTokens/full/delta。
	ResultID string `json:"resultId"`
}

// InlayHint 是服务器为文档提供的一条内联注解。
type InlayHint struct {
	Position Position `json:"position"`
	// Label 是展示给用户的文本。线上来的 Label parts 被解析器展平为纯文本。
	// 被解析器展平为纯文本。
	Label        string `json:"label"`
	Kind         int    `json:"kind,omitempty"`
	Tooltip      string `json:"tooltip,omitempty"`
	PaddingLeft  bool   `json:"paddingLeft,omitempty"`
	PaddingRight bool   `json:"paddingRight,omitempty"`
}

// CodeLens 是附加到某区间的可点击标签。IsResolved 由
// CodeLens 是附加到某区间的可点击标签。IsResolved 由客户端推导：没有 command 的 lens 仍需要 codeLens/resolve。
type CodeLens struct {
	Range      Range    `json:"range"`
	Command    *Command `json:"command,omitempty"`
	IsResolved bool     `json:"-"`
}

// severityName 把数字 LSP 严重性映射为词。
func severityName(severity int) string {
	switch severity {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "information"
	case 4:
		return "hint"
	default:
		return "error"
	}
}
