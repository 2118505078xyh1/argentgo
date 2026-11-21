package llm

// cacheStats 汇总一次请求的提示缓存统计，供用量报告暴露缓存命中率以及
// 缓存读取/写入 token。它只做数值裁剪与比值计算，不接触网络、客户端状态
// 或任何字符串，因此是纯函数：相同入参始终返回相同键值，且不修改外部状态。
//
// 入参语义（与 Usage 的计费口径一致）：input 是本次请求计入计费的提示 token
// 总数，已包含缓存读取与缓存写入的部分；cachedRead 是其中由缓存命中的读取
// token；cachedWrite 是写入缓存的 token。
//
// 返回的 map 始终包含以下固定键（键名与区域设置无关）：
//
//   - hitRate：float64，命中率 = readTokens / totalTokens，取值 0..1；
//     分母为零时取 0，绝不产生 NaN 或 +Inf。
//   - readTokens：int，缓存读取 token，裁剪到 0..totalTokens。
//   - writeTokens：int，缓存写入 token，裁剪到 0..totalTokens。
//   - totalTokens：int，提示 token 总计，即裁剪到非负的 input。
//
// 边界处理：负数一律按 0 处理；cachedRead 不应超过 input，越界时按 totalTokens
// 截断，命中率因此自然落在 0..1；cachedWrite 同样按 totalTokens 截断以保证有界。
// 全程没有加法或乘法，极端入参（如 math.MaxInt）也不会整数溢出。由于不涉及
// 任何字符串或排序，本函数对 CJK 与非 CJK 环境行为完全一致。
func cacheStats(input, cachedRead, cachedWrite int) map[string]any {
	total := input
	if total < 0 {
		total = 0
	}

	read := cachedRead
	if read < 0 {
		read = 0
	}
	if read > total {
		read = total
	}

	write := cachedWrite
	if write < 0 {
		write = 0
	}
	if write > total {
		write = total
	}

	rate := 0.0
	if total > 0 {
		rate = float64(read) / float64(total)
	}

	return map[string]any{
		"hitRate":     rate,
		"readTokens":  read,
		"writeTokens": write,
		"totalTokens": total,
	}
}
