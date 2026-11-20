package session

// 模型快照载荷使用的稳定键。键名固定为英文，保证跨版本、跨语言的转录
// 读取方都能用同一组字段恢复会话所用的 provider 与 model。
const (
	modelSnapshotProviderKey = "provider"
	modelSnapshotModelKey    = "model"
)

// modelSnapshotRecord 构建可记录到 JSONL 的模型快照载荷。
//
// 它是纯函数：不读取全局状态，不修改入参，每次调用都返回全新的 map。
// 只包含非空的 provider/model，因此两个入参都为空时得到非 nil 的空 map
// （编码为 `{}` 而不是 `null`），让调用方可以无条件地把它交给 Append。
//
// 键名固定且由 encoding/json 按字典序输出，所以相同输入总会得到相同的字节；
// 值是普通 Go 字符串，CJK 文本会原样写成 UTF-8，不会被转义成 \uXXXX 序列。
func modelSnapshotRecord(provider, model string) map[string]string {
	payload := make(map[string]string, 2)
	if provider != "" {
		payload[modelSnapshotProviderKey] = provider
	}
	if model != "" {
		payload[modelSnapshotModelKey] = model
	}
	return payload
}
