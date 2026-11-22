package tools

import (
	"sort"
	"time"
)

// TimeoutTier 表示工具超时预算的粗粒度分级，供诊断与文档展示使用。
//
// 本文件是一组纯函数、零运行时调用方的只读原语：它不替换
// internal/tools/tool-timeouts.go 中的运行时超时表，也不参与任何工具调用
// 的 deadline 计算，因此不改变现有行为。分级值取自给诊断/文档约定的固定档位，
// 其中 long 与运行时表中 bash 的默认值（120s）一致；运行时表中 fetch、
// web_search、git_status 的 20s 落在 fast(10s) 与 normal(30s) 之间，
// 没有与之逐一相等的档位——这是刻意的：本分级只做粗粒度归类，不做精确对齐。
type TimeoutTier string

const (
	// TimeoutFast 是轻量只读操作的档位（10s）。
	TimeoutFast TimeoutTier = "fast"
	// TimeoutNormal 是默认档位（30s），也是未知工具与未知档位的回退值。
	TimeoutNormal TimeoutTier = "normal"
	// TimeoutLong 是命令执行等长时操作的档位（120s）。
	TimeoutLong TimeoutTier = "long"
	// TimeoutBatch 是多文件/批量操作的档位（300s）。
	TimeoutBatch TimeoutTier = "batch"
)

// timeoutTierOrder 是各档位的稳定展示顺序（由快到慢）。
// DescribeTimeouts 严格按此顺序输出，保证确定性。
var timeoutTierOrder = []TimeoutTier{TimeoutFast, TimeoutNormal, TimeoutLong, TimeoutBatch}

// timeoutTierDurations 保存各档位的预算值。未知档位回退到 TimeoutNormal 的值。
var timeoutTierDurations = map[TimeoutTier]time.Duration{
	TimeoutFast:   10 * time.Second,
	TimeoutNormal: 30 * time.Second,
	TimeoutLong:   120 * time.Second,
	TimeoutBatch:  300 * time.Second,
}

// timeoutTierTools 把已知工具名显式归入某一档。
// 清单是诊断用的开放集合，不要求覆盖全部工具：未命中的名字一律归 TimeoutNormal。
// 匹配精确且大小写敏感（不做 trim/小写化），避免形如 "bash " 的冒名工具混入。
var timeoutTierTools = map[string]TimeoutTier{
	// fast：有界、快速的本地只读查询。
	"read":         TimeoutFast,
	"ls":           TimeoutFast,
	"glob":         TimeoutFast,
	"grep":         TimeoutFast,
	"find":         TimeoutFast,
	"git_status":   TimeoutFast,
	"current_time": TimeoutFast,

	// normal：常规编辑与网络读取，也是所有未知名工具的默认档。
	"edit":       TimeoutNormal,
	"write":      TimeoutNormal,
	"fetch":      TimeoutNormal,
	"web_fetch":  TimeoutNormal,
	"web_search": TimeoutNormal,
	"grep_ex":    TimeoutNormal,

	// long：任意命令执行，耗时上限最高。
	"bash":       TimeoutLong,
	"shell":      TimeoutLong,
	"powershell": TimeoutLong,
	"exec":       TimeoutLong,

	// batch：多文件/批量操作。
	"batch_files": TimeoutBatch,
	"multi_edit":  TimeoutBatch,
	"apply_patch": TimeoutBatch,
	"repo_map":    TimeoutBatch,
}

// TimeoutForTier 返回档位对应的预算时长。
// 未知档位回退到 TimeoutNormal（30s），保证任何输入都有确定结果。
func TimeoutForTier(tier TimeoutTier) time.Duration {
	if d, ok := timeoutTierDurations[tier]; ok {
		return d
	}
	return timeoutTierDurations[TimeoutNormal]
}

// ClassifyToolTimeout 把工具名归入一个超时档位。
// 未收录的工具名（含含 CJK 字符的自定义工具名、空串）一律归 TimeoutNormal。
// 只做字符串查表，不拆分 rune、不做截断，因此对任意 UTF-8 输入都安全。
func ClassifyToolTimeout(toolName string) TimeoutTier {
	if tier, ok := timeoutTierTools[toolName]; ok {
		return tier
	}
	return TimeoutNormal
}

// TimeoutTierDescription 描述一个超时档位的预算与归属工具，供文档/doctor 展示。
type TimeoutTierDescription struct {
	Tier    TimeoutTier
	Seconds int
	Tools   []string
}

// DescribeTimeouts 返回所有档位的稳定描述：档位按 timeoutTierOrder 排列，
// 每个档位的工具名按字典序排列。每次调用都返回全新的切片与工具名切片，
// 调用方修改结果不会影响后续调用。
func DescribeTimeouts() []TimeoutTierDescription {
	out := make([]TimeoutTierDescription, 0, len(timeoutTierOrder))
	for _, tier := range timeoutTierOrder {
		tools := make([]string, 0)
		for name, t := range timeoutTierTools {
			if t == tier {
				tools = append(tools, name)
			}
		}
		sort.Strings(tools)
		out = append(out, TimeoutTierDescription{
			Tier:    tier,
			Seconds: int(timeoutTierDurations[tier] / time.Second),
			Tools:   tools,
		})
	}
	return out
}
