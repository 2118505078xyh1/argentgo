package session

// 省略记录载荷使用的稳定键。键名固定为英文，保证跨版本、跨语言的转录
// 读取方都能用同一组字段识别这是一条"省略某条记录及其结果"的编辑指令。
// 值 "omit" 与 "id_and_results" 同样固定，便于投影方做精确字符串比较。
const (
	omissionActionKey = "action"
	omissionScopeKey  = "scope"
	omissionTargetKey = "target"

	omissionActionOmit       = "omit"
	omissionScopeIDAndResult = "id_and_results"
)

// omissionRecord 构建可写入 JSONL 的"省略记录"载荷：要求模型投影跳过
// targetID 指向的记录以及由它产生的工具结果，而原始 JSONL 记录本身保持不变。
// 这是恢复审计的纯能力构建块： Argent-go 目前没有 context_edit 机制，
// 因此本函数只生成载荷，不负责把它接入消息投影（接线点见相关报告）。
//
// 它是纯函数：不读取全局状态，不修改入参，每次调用都返回全新的 map。
// targetID 为空时返回非 nil 的空 map（编码为 `{}` 而不是 `null`），
// 让调用方可以无条件地把它交给 Append。
//
// 键名固定且由 encoding/json 按字典序输出，所以相同输入总会得到相同的字节；
// 值是普通 Go 字符串，CJK 文本会原样写成 UTF-8，不会被转义成 \uXXXX 序列。
func omissionRecord(targetID string) map[string]string {
	payload := make(map[string]string, 3)
	if targetID == "" {
		return payload
	}
	payload[omissionActionKey] = omissionActionOmit
	payload[omissionScopeKey] = omissionScopeIDAndResult
	payload[omissionTargetKey] = targetID
	return payload
}
