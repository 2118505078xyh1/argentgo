package eval

// 本文件提供构造与评估"长对话不跑偏"场景的纯原语。
// 两个函数都不修改入参、不依赖时钟/随机数/全局状态，也不触碰 golden 评分路径；
// 输出仅由入参决定（干扰内容由 noise 注入），便于跨进程、跨版本稳定比对。

// TurnSim 是长对话模拟中的一条消息，只携带构造与统计所需的最小字段。
// 它与 MessageLike 结构相同但独立定义，使长对话原语保持自洽、零依赖。
type TurnSim struct {
	Role       string
	Content    string
	ToolResult bool
}

// defaultRepeat 是无效 repeat（<= 0）的回退值：负数和零没有模拟意义，
// 退化为单轮仍能产出可用的最小场景，避免调用方拿到空结果而无法评估。
const defaultRepeat = 1

// SimulateLongConversation 把 base 重复 repeat 次，并在每轮末尾插入 noise(i) 产生的干扰消息，
// 用以模拟长对话中不断累积的历史与噪声。它是纯函数：
//   - 不修改 base 或其中的字符串，返回的是新建切片，不与 base 共享底层数组；
//   - 不依赖时钟、随机数或全局状态；
//   - 干扰消息完全由 noise 决定，因此相同入参与确定性 noise 总产生相同结果。
//
// 边界行为：
//   - repeat <= 0 时按 defaultRepeat 回退为单轮（上限保护/非法值兜底）；
//   - noise 为 nil 时不插入任何干扰消息，只重复 base；
//   - base 为空时仍按轮次插入 noise 产生的干扰消息（每轮一条）。
func SimulateLongConversation(base []TurnSim, repeat int, noise func(i int) TurnSim) []TurnSim {
	if repeat <= 0 {
		repeat = defaultRepeat
	}

	perRound := len(base)
	if noise != nil {
		perRound++
	}

	// 容量提示只在乘法不溢出 int 时使用，避免极端 repeat 触发 make 的负容量 panic。
	// 这只影响预分配，不改变任何语义。
	capHint := 0
	if perRound > 0 && repeat <= int(^uint(0)>>1)/perRound {
		capHint = repeat * perRound
	}

	out := make([]TurnSim, 0, capHint)
	for i := 0; i < repeat; i++ {
		out = append(out, base...)
		if noise != nil {
			out = append(out, noise(i))
		}
	}
	return out
}

// ConversationStats 统计一组对话轮次的基本量：
//   - total：消息总数；
//   - byRole：各 Role 的消息条数，键为 Role 原值（空 Role 也会计入）；
//   - toolResults：ToolResult 为 true 的消息条数；
//   - uniqueContent：不同 Content 的种类数，Content 按字节精确去重，空串也算一种。
//
// 它是纯函数，不修改 turns。Content 按 Go 字符串逐字节比较，不会切分 UTF-8，
// 因此 CJK/emoji 安全：不同文本不会被误判为相同，多字节序列也不会被截断。
// 空输入时 byRole 返回非 nil 的空 map，便于调用方直接写入。
func ConversationStats(turns []TurnSim) (total int, byRole map[string]int, toolResults int, uniqueContent int) {
	byRole = make(map[string]int)
	seen := make(map[string]struct{})
	for _, t := range turns {
		total++
		byRole[t.Role]++
		if t.ToolResult {
			toolResults++
		}
		seen[t.Content] = struct{}{}
	}
	uniqueContent = len(seen)
	return
}
