package provider

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// CapabilityProfile 是一份只读的模型能力画像：把散落在各协议代码里的能力
// （思考、图片输入、工具调用、上下文窗口）汇总成一个可自检的不可变快照。
//
// 它供 provider 自检/doctor 等诊断用途使用；本文件只提供纯函数原语，
// 不改变线上请求字节。CLI 的 doctor 通过只读诊断旁路消费 ProfileFor 的画像。
type CapabilityProfile struct {
	// Name 是规范化后的 provider 名：已知模板取规范名，未知取调用方传入的 templateName 原值。
	Name string
	// SupportsThinking 报告该 provider 的线上协议是否具备推理控制能力。
	SupportsThinking bool
	// SupportsImage 报告模型名是否命中已知的多模态/视觉模型标记。
	SupportsImage bool
	// SupportsTools 报告是否支持工具调用。当前所有内置协议都支持，故恒为 true。
	SupportsTools bool
	// ContextWindow 是推导出的上下文窗口 token 数；未知时按回落链得到 0。
	ContextWindow int
}

// supportsImage 转发到 llm.VisionMarkerFor，与线上媒体提示共用同一份判定，
// 不再维护自己的快照表（消除两表漂移）。纯函数，CJK 安全。
func supportsImage(model string) bool {
	return llm.VisionMarkerFor(model)
}

// ProfileFor 由 provider 模板名与模型名组合出一份能力画像。
//
// 它复用包内已有的纯函数：Resolve（模板/别名解析）、ThinkingSupport（协议推理能力）、
// ContextWindowFor（上下文窗口回落链），不重造这些逻辑。决策如下：
//
//   - Name：已知模板取规范名（大小写/空白/别名归一），未知则原样保留 templateName。
//   - SupportsThinking：协议具备任一推理控制形态即为 true；未知模板为 false。
//   - SupportsImage：模型名命中已知视觉标记才为 true；未指定模型时回落到模板默认模型。
//   - SupportsTools：恒为 true（所有内置协议都支持工具调用，未知也按保守可用处理）。
//   - ContextWindow：走 ContextWindowFor(model, 0) 的既有回落链（模型 → 模板默认 → 0）。
//
// 未知 provider/模型得到保守默认：tools=true，其余 false，window 来自
// KnownContextWindow 或 0。这是纯函数：无 I/O、不改入参（字符串不可变）、
// 不依赖可变全局状态，结果确定且可并发调用；templateName/model 含 CJK 时原样安全处理。
func ProfileFor(templateName, model string) CapabilityProfile {
	profile := CapabilityProfile{
		Name:          templateName,
		SupportsTools: true,
	}

	resolved, known := Resolve(templateName)
	if known {
		profile.Name = resolved.Name
		profile.SupportsThinking = ThinkingSupport(string(resolved.Protocol)) != ThinkingNone
	}

	effectiveModel := model
	if strings.TrimSpace(effectiveModel) == "" && known {
		effectiveModel = resolved.DefaultModel
	}
	profile.SupportsImage = supportsImage(effectiveModel)
	profile.ContextWindow = ContextWindowFor(templateName, model, 0)
	return profile
}
