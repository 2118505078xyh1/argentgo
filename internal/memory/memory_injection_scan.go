package memory

import (
	"regexp"
	"sort"
	"strings"

	"example.com/argentgo/internal/security"
)

// 本文件提供一个纯检测/清洗原语，供上层在把项目指令文档（AGENTS.md /
// CLAUDE.md 等内容）注入提示词之前调用。
//
// 不改变注入策略：memory 文档在 system prompt 中仍按 R30 的免责声明被当作
// "启发式指导、非权威来源"（见 Disclaimer）；本原语只是在注入前多一道可选的
// 检测与中和手段，既不改变 Block/BlockSafe 的渲染，也不添加新的拒绝路径。
//
// 不接线：本文件不修改任何实际注入路径。调用方若需要防护，应自行在注入前
// 调用 ScanMemoryInjection / SanitizeMemoryForInjection。

// SanitizeMarker 是 SanitizeMemoryForInjection 用于替换命中片段的中性标记。
// 它不含祈使动词或角色词，因此清洗后的文本不会再次被注入检测器命中。
const SanitizeMarker = "[已过滤可疑指令]"

// spanPattern 把 security 侧的注入模式镜像为可给出字节区间的正则。
// name 仅用于与 security.InjectionFinding.Pattern 对照（一致性测试会校验）。
type spanPattern struct {
	name string
	re   *regexp.Regexp
}

// memorySpanPatterns 是 security.promptInjectionScanPatterns 的精确镜像，
// 正则字面量逐字复制自 internal/security/prompt_injection_scan.go。
//
// 为什么需要镜像：security.ScanPromptInjection 只暴露命中的起始字节偏移
// （InjectionFinding.Offset），不暴露结束偏移/长度；而清洗必须知道完整区间
// 才能替换"对应片段"。因此在 memory 侧保留一份同样的正则，仅用于求区间。
// 两份正则的一致性由 TestMemorySpanPatternsMatchSecurity 钉死：若 security
// 侧将来修改模式而这里没跟上，该测试会红，提醒同步更新。
var memorySpanPatterns = []spanPattern{
	{
		"en:ignore-previous-instructions",
		regexp.MustCompile(`(?i)\b(?:ignore|disregard|forget|override|bypass|skip)\s+(?:(?:all|any|the|your|these|those|previous|prior|earlier|above|preceding)\s+){0,3}(?:instructions?|rules?|prompts?|directions?|guidelines?|commands?|constraints?|restrictions?)\b`),
	},
	{
		"en:system-prompt-disclosure",
		regexp.MustCompile(`(?i)\b(?:reveal|print|show|display|disclose|dump|repeat|output|write\s+out|tell\s+me|ignore|override|bypass)\s+(?:me\s+)?(?:(?:the|your|full|entire|original|hidden|exact)\s+){0,3}system\s+(?:prompt|message|instructions?)\b`),
	},
	{
		"en:role-escalation-you-are-now",
		regexp.MustCompile(`(?i)\byou are now\s+(?:(?:a|an|the|in)\s+)?(?:developer|admin(?:istrator)?|assistant|dan|superuser|root|unrestricted|unfiltered|jailbroken|debug(?:ger)?|engineer|persona|expert|owner|god|mode)\b`),
	},
	{
		"en:roleplay-escalation",
		regexp.MustCompile(`(?i)\b(?:act\s+as|pretend\s+(?:you\s+are|to\s+be)|roleplay\s+as|play\s+the\s+role\s+of)\s+(?:(?:a|an|the)\s+)?(?:unrestricted|unfiltered|jailbroken|dan|developer|admin(?:istrator)?|root|superuser|system|god|evil|amoral)\b`),
	},
	{
		"zh:ignore-above-instructions",
		regexp.MustCompile(`(?:忽略|无视|忘记|跳过)\s*(?:(?:上述|以上|前面|之前|先前|上面|刚刚|这段|本段)(?:\s*的)?\s*(?:(?:所有|全部|一切)\s*)?(?:指令|指示|规则|提示词|提示|命令|要求|约束|限制|内容|消息)?|(?:所有|全部|一切)\s*(?:的)?\s*(?:指令|指示|规则|提示词|提示|命令|要求|约束|限制|内容|消息))`),
	},
	{
		"zh:system-prompt-disclosure",
		regexp.MustCompile(`(?i)(?:泄露|输出|打印|显示|展示|给出|忽略|绕过|重复|告诉我|写出|公布)\s*(?:(?:你|完整|全部|隐藏|原始|系统)的?)?\s*(?:系统提示词|系统提示|系统指令|系统消息|system\s+prompt|system\s+message)`),
	},
	{
		"zh:role-escalation",
		regexp.MustCompile(`(?i)(?:你现在是|从现在起你是|从现在开始你是|现在你是|你现在扮演|你扮演)\s*(?:一个?|一位)?\s*(?:开发者|开发模式|管理员|调试模式?|无限制|未过滤|超级用户|助手|专家|老板|主人|root|dan|系统|越权|god)`),
	},
	{
		"zh:roleplay-escalation",
		regexp.MustCompile(`(?i)(?:扮演|假装你是|假装自己是|假设你是|模拟你是)\s*(?:一个?|一位)?\s*(?:管理员|开发者|无限制(?:的)?(?:助手|模式|AI)?|未过滤|dan|root|超级用户|系统|越权|god|jailbroken)`),
	},
}

// byteSpan 是左闭右开的字节区间 [start, end)，均指向 content 的 UTF-8 字节边界。
type byteSpan struct {
	start int
	end   int
}

// memoryInjectionSpans 返回 content 中所有注入模式命中的字节区间，按 start
// 升序排列，重叠（含相接）区间被合并，避免重复替换与下标漂移。
func memoryInjectionSpans(content string) []byteSpan {
	if content == "" {
		return nil
	}
	var spans []byteSpan
	for _, p := range memorySpanPatterns {
		for _, loc := range p.re.FindAllStringIndex(content, -1) {
			spans = append(spans, byteSpan{start: loc[0], end: loc[1]})
		}
	}
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end < spans[j].end
	})
	merged := spans[:0]
	for _, s := range spans {
		if len(merged) > 0 && s.start <= merged[len(merged)-1].end {
			last := &merged[len(merged)-1]
			if s.end > last.end {
				last.end = s.end
			}
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

// ScanMemoryInjection 检测 content 中命中的注入模式，返回去重后按字典序升序
// 排列的模式标识列表（例如 "en:ignore-previous-instructions"）。
//
// 检测直接复用 security.ScanPromptInjection，因此与 security 的语义注入判定
// 完全一致：只识别"动作动词 + 指令类名词 / 角色词"的祈使语境，孤立关键词与
// 正常讨论不会命中。无命中返回 nil；结果稳定、确定。
func ScanMemoryInjection(content string) []string {
	findings := security.ScanPromptInjection(content)
	if len(findings) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(findings))
	for _, f := range findings {
		set[f.Pattern] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// SanitizeMemoryForInjection 在 content 命中注入模式时，把每个命中片段替换为
// SanitizeMarker，返回清洗后的文本与实际替换的片段数；未命中时原样返回
// (content, 0)。
//
// 性质：
//   - 不改入参：Go 字符串不可变，函数始终返回新构造的字符串。
//   - CJK 安全：所有切分都按 security/regexp 给出的 UTF-8 字节边界进行，
//     绝不在多字节字符中间切开。
//   - 字节有界：输出长度 = len(content) - 被删字节 + n*len(SanitizeMarker)，
//     随输入线性增长，不会因清洗而膨胀为超线性规模。
//
// 先用 security.ScanPromptInjection 判定是否需要清洗（与 ScanMemoryInjection
// 同一检测源），再用内存侧的精确区间镜像替换对应片段。
func SanitizeMemoryForInjection(content string) (string, int) {
	if len(security.ScanPromptInjection(content)) == 0 {
		return content, 0
	}
	spans := memoryInjectionSpans(content)
	if len(spans) == 0 {
		// 检测源与区间镜像不一致（例如 security 侧新增了模式）——保守起见
		// 原样返回，绝不静默丢弃内容；一致性测试负责让这种情况变红。
		return content, 0
	}
	var b strings.Builder
	b.Grow(len(content) + len(spans)*len(SanitizeMarker))
	cursor := 0
	replaced := 0
	for _, s := range spans {
		// 区间来自同一 content 的正则匹配，必然落在有效字节边界且单调不减；
		// 这里仍做防御性校验，异常区间不参与替换。
		if s.start < cursor || s.start > s.end || s.end > len(content) {
			continue
		}
		b.WriteString(content[cursor:s.start])
		b.WriteString(SanitizeMarker)
		cursor = s.end
		replaced++
	}
	b.WriteString(content[cursor:])
	return b.String(), replaced
}
