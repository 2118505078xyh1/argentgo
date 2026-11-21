package tools

import (
	"strconv"
	"strings"
	"unicode"
)

// Provenance 描述一次工具结果在返回给模型之前的来源与处理痕迹。
//
// 它是一个纯数据原语：不参与任何现有输出管线，也没有生产调用方。
// 四个字段各自回答一个可观测性问题——结果来自哪个工具、是否被脱敏、
// 是否被裁剪、最终字节数是多少。
type Provenance struct {
	Tool      string
	Sanitized bool
	Clipped   bool
	Bytes     int
}

// AnnotateResult 由工具名与原始输出构造来源标注，是纯函数。
//
// Bytes 取输出的字节数而非 rune 数：CJK、emoji 等多字节字符按实际
// 占用字节计数，便于与裁剪/预算等以字节为准的环节对齐。
// len 作用在 string 上返回的正是字节数，等价于 len([]byte(output))，
// 但省去一次不必要的切片分配。
func AnnotateResult(tool, output string, sanitized, clipped bool) Provenance {
	return Provenance{
		Tool:      tool,
		Sanitized: sanitized,
		Clipped:   clipped,
		Bytes:     len(output),
	}
}

// Header 生成稳定的单行来源头，例如：
//
//	[tool:read sanitized clipped bytes=123]
//
// 字段顺序固定为 tool、sanitized、clipped、bytes：两个布尔标记按需出现，
// 但相对次序永不改变，bytes 永远在末尾。因此同一 Provenance 反复调用
// 必得同一字符串，可安全用于 golden 断言与日志去重。
//
// 工具名经 escapeProvenanceTool 处理：控制字符与方括号转义为 \xNN，
// 保证结果不含换行、始终是单行且字段边界无歧义；其余字符（含 CJK）
// 按 rune 原样保留，所以合法 UTF-8 输入得到合法 UTF-8 头。
func (p Provenance) Header() string {
	var b strings.Builder
	b.WriteString("[tool:")
	b.WriteString(escapeProvenanceTool(p.Tool))
	if p.Sanitized {
		b.WriteString(" sanitized")
	}
	if p.Clipped {
		b.WriteString(" clipped")
	}
	b.WriteString(" bytes=")
	b.WriteString(strconv.Itoa(p.Bytes))
	b.WriteByte(']')
	return b.String()
}

// escapeProvenanceTool 转义会破坏单行格式或字段边界的字符。
// 逐个 rune 遍历，非 ASCII 字符（如 CJK）原样写出，因此不会在字节
// 中途切断 UTF-8 序列；输入中的非法 UTF-8 字节会被替换为 RuneError，
// 反而保证输出始终是合法 UTF-8。
func escapeProvenanceTool(tool string) string {
	var b strings.Builder
	for _, r := range tool {
		if r == '[' || r == ']' || unicode.IsControl(r) {
			hex := strconv.FormatInt(int64(r), 16)
			if len(hex) < 2 {
				hex = "0" + hex
			}
			b.WriteString("\\x")
			b.WriteString(hex)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// PrependHeader 把来源头拼到 output 之前，二者用单个换行分隔。
// 它只做拼接，绝不裁剪、转义或规范化 output：返回值的后缀与 output
// 逐字节相同。header 为空时直接返回 output，不引入多余的前导换行。
func PrependHeader(header, output string) string {
	if header == "" {
		return output
	}
	return header + "\n" + output
}
