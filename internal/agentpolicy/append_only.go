// Append-only Context / StablePrefix 的纯观测层。
//
// 本文件沿用 NormalizeToolPairing 的“纯视图”范式：只读取消息、不修改任何输入、
// 不做 I/O、不持有状态，也不接入 agent 循环的任何检查点。调用方可在需要时
// （例如会话恢复或提示缓存决策处）自行观测一个消息视图是否保持了稳定前缀，
// 仓库当前没有调用方。
package agentpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// stablePrefixDigestSeed 让摘要与协议自身的 JSON 编码解耦：即使 llm.Message
// 的 JSON 标签将来调整，已落库的旧摘要也不会与新算法产出的值意外碰撞。
const stablePrefixDigestSeed = "agentpolicy.StablePrefixView/v1\x00"

// chainDigests 返回长度为 len(messages)+1 的中间摘要序列。
//
// 第 k 项是“前 k 条消息”的摘要：第 0 项是种子摘要，第 k+1 项由第 k 项与
// 第 k 条消息的规范 JSON 字节链接得到。正是这种可分解的链式结构，使调用方
// 仅凭一个旧摘要（prevDigest）就能判断新视图是否包含旧视图作为前缀——普通
// 的整体 SHA256 做不到这一点。
func chainDigests(messages []llm.Message) []string {
	out := make([]string, 0, len(messages)+1)
	chain := sha256.Sum256([]byte(stablePrefixDigestSeed))
	out = append(out, hex.EncodeToString(chain[:]))

	for i := range messages {
		// llm.Message 全由可 JSON 序列化的字段组成，error 恒为 nil；
		// 字段顺序固定，因此同一条消息的编码字节是确定的。CacheControl
		// 带 json:"-" 标签，是网关的临时标记，本就不属于稳定转录内容。
		encoded, _ := json.Marshal(messages[i])
		h := sha256.New()
		h.Write(chain[:])
		h.Write(encoded)
		copy(chain[:], h.Sum(nil))
		out = append(out, hex.EncodeToString(chain[:]))
	}
	return out
}

// DigestOf 返回 messages 的稳定摘要：同一种消息序列永远得到同一结果，
// 任意一条消息的内容、顺序或数量变化都会改变结果。
func DigestOf(messages []llm.Message) string {
	digests := chainDigests(messages)
	return digests[len(digests)-1]
}

// StablePrefixView 观测一个消息视图是否相对旧视图做了严格前缀扩展。
//
// view 是 messages 的只读引用（函数从不写入它），frozenSystem 是以字节比较的
// 冻结系统提示。判定规则：
//   - 若首条 system 消息的内容与 frozenSystem 字节不等，说明稳定前缀被改写，
//     直接判为非追加；
//   - prevDigest 为空时视为首次观测，建立基线并判为可追加；
//   - 否则在视图的前缀链中寻找与 prevDigest 相等的位置 k，仅当 k 严格小于
//     当前长度（即确实新增了消息）时判为严格前缀扩展。
//
// 返回的 digest 是当前视图的摘要，可作为下一次调用传入的 prevDigest。
func StablePrefixView(messages []llm.Message, frozenSystem, prevDigest string) (view []llm.Message, isAppendOnly bool, digest string) {
	view = messages // 只读引用：保持“纯视图”语义，绝不修改调用方的切片或元素
	digests := chainDigests(view)
	digest = digests[len(digests)-1]

	// 冻结前缀被改写：无论哈希链是否匹配，都不能视为追加。
	if len(view) > 0 && view[0].Role == "system" && view[0].Content != frozenSystem {
		return view, false, digest
	}

	// 首次观测没有历史可比对，建立基线。
	if prevDigest == "" {
		return view, true, digest
	}

	// 旧摘要必须出现在某个“严格更短”的前缀上；若只等于整段摘要，
	// 说明视图没有任何增长，不满足“严格”扩展。
	for k := 0; k < len(view); k++ {
		if digests[k] == prevDigest {
			return view, true, digest
		}
	}
	return view, false, digest
}
