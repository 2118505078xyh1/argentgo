package app

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// 命令审批呈现的规范化上限。它们是有界的固定值，保证 reason 与 Summary 的
// 命令预览无论输入多长都不会无限膨胀；截断均按 rune 进行，因此对 CJK 安全，
// 绝不会在 UTF-8 字节中间切开，也不会产生替换字符。
const (
	commandPresentationReasonLimit  = 240 // reason 最多保留的 rune 数（超出追加省略号）
	commandPresentationPreviewLimit = 120 // Summary 中命令预览最多保留的 rune 数
)

// 摘要文案的固定字面量。全部为简体中文常量，便于审批 UI/模型呈现复用，
// 也方便测试断言不依赖具体排版细节。
const (
	commandPresentationLabel        = "命令审批"
	commandPresentationRiskLabel    = "风险"
	commandPresentationWorkdirLabel = "目录"
	commandPresentationCommandLabel = "命令"
	commandPresentationReasonLabel  = "原因"
	commandPresentationRiskUnknown  = "未标注"
	commandPresentationEllipsis     = "…"
	commandPresentationSeparator    = "｜"
)

// CommandPresentation 是一条命令进入审批环节时的规范化呈现。所有字段都已去除
// 首尾空白；Reason 已做有界截断；Lines 记录命令正文的展示行数。该结构体不包含
// 任何审批链状态，只是一个可被审批 UI/模型呈现复用的纯数据快照。
type CommandPresentation struct {
	Command string // 去除首尾空白后的命令正文
	Workdir string // 去除首尾空白后的工作目录
	Risk    string // 去除首尾空白后的风险标记
	Reason  string // 去除首尾空白并做有界截断后的原因
	Lines   int    // 命令正文的展示行数（换行数 + 1，空命令按 1 行计）
}

// PresentCommand 规范化一条命令的审批呈现。它只依赖四个入参：对每个字段做
// strings.TrimSpace 去除首尾空白，对 reason 做 rune 安全的有界截断，并计算
// 命令正文的展示行数。它是纯函数——不读时钟、不访问全局状态、不产生副作用，
// 且 Go 字符串不可变，因此绝不修改入参。
//
// 截断规则：先去首尾空白，再数 rune。rune 数不超过上限时原样保留；超出时保留
// 前 limit 个 rune 再追加一个省略号。按 rune 切片意味着 CJK 字符不会被切开，
// 结果始终是合法 UTF-8，也不会出现半个汉字或替换字符。
//
// Lines 表示命令正文的展示行数：归一化后的命令含 k 个换行则为 k+1 行，空命令
// 按 1 行计（审批卡片仍要占一行）。Workdir/Risk/Reason 作为单行标签展示，
// 不计入 Lines。CRLF 中的换行符同样被计数。
func PresentCommand(command, workdir, risk, reason string) CommandPresentation {
	cmd := strings.TrimSpace(command)
	return CommandPresentation{
		Command: cmd,
		Workdir: strings.TrimSpace(workdir),
		Risk:    strings.TrimSpace(risk),
		Reason:  truncateRunes(strings.TrimSpace(reason), commandPresentationReasonLimit),
		Lines:   strings.Count(cmd, "\n") + 1,
	}
}

// Summary 生成一段稳定的中文摘要，供审批 UI 或模型呈现复用。摘要固定包含风险
// 标记与命令的有界预览，并在对应字段非空时附带目录与原因。它是纯函数：只看
// 接收者的五个字段，因此对相同呈现总是返回相同字节序列。
//
// 稳定性来源：
//   - 风险为空时统一写作“未标注”，不输出空标记；
//   - 命令正文先经 strings.Fields 把任意连续空白（含换行、全角空白）折叠为单个
//     半角空格，得到单行预览，再做 rune 安全的有界截断，因此多行命令不会破坏
//     摘要的单行结构；
//   - 原因再次截断到与 PresentCommand 相同的上限，保证即使接收者被直接构造，
//     摘要长度仍然有界；
//   - 可选段只在字段非空时出现，段间用固定分隔符连接，不产生悬空分隔符。
func (p CommandPresentation) Summary() string {
	risk := p.Risk
	if risk == "" {
		risk = commandPresentationRiskUnknown
	}
	preview := truncateRunes(flattenOneLine(p.Command), commandPresentationPreviewLimit)

	parts := []string{
		"【" + commandPresentationLabel + "】" + commandPresentationRiskLabel + "：" + risk,
		commandPresentationCommandLabel + "：" + preview,
	}
	if p.Workdir != "" {
		parts = append(parts, commandPresentationWorkdirLabel+"："+p.Workdir)
	}
	if reason := truncateRunes(p.Reason, commandPresentationReasonLimit); reason != "" {
		parts = append(parts, commandPresentationReasonLabel+"："+reason)
	}
	return strings.Join(parts, commandPresentationSeparator)
}

// RedactCommandSecrets 用 security.Redact 脱敏命令中的密钥形状（如
// KEY=xxx、token、sk-... 等），并把 Unicode Tag 不可见字符一并剥离。它是纯函数：
// 只依赖入参，不修改输入字符串，也不触碰任何全局审批状态。
//
// 它刻意不接审批链、不改写 CommandPresentation——调用方若需要在呈现前脱敏，
// 可自行组合：PresentCommand(RedactCommandSecrets(raw), ...)。
func RedactCommandSecrets(command string) string {
	return security.Redact(command)
}

// truncateRunes 按 rune 对 s 做有界截断：rune 数不超过 limit 时原样返回，否则
// 保留前 limit 个 rune 并追加一个省略号。limit <= 0 时返回空串。按 rune 切片
// 保证 CJK 安全，绝不切开 UTF-8 字节序列。
func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + commandPresentationEllipsis
}

// flattenOneLine 把任意连续空白（含换行、制表符与全角空白）折叠为单个半角空格，
// 得到稳定的单行文本，用于 Summary 的命令预览。strings.Fields/Join 按 rune 处理
// Unicode 空白，因此对 CJK 安全。
func flattenOneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
