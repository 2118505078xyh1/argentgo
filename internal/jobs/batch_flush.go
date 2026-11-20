package jobs

import "time"

// shouldFlushBatch 判断一个待写批次是否应触发落盘（flush）。
//
// 判定是纯函数：不读写任何状态、不产生副作用，仅比较传入的计数与阈值。
// 三条触发规则彼此独立，任一"已启用"的规则被达到（含恰好相等）即返回 true：
//   - 条数：count >= maxCount；
//   - 字节：bytes >= maxBytes；
//   - 时间：elapsed >= maxDelay。
//
// 约定与边界：
//   - 阈值 <= 0 视为关闭该维度，调用方可用零值/负值停用某条规则；三条都关闭时
//     返回 false。需要强制落盘（例如关闭时刷尾部）时由调用方显式处理。
//   - bytes 是原始字节数而非 rune 数，因此对 CJK/emoji 等多字节字符不敏感：
//     同一段文本无论字符宽度，都按实际字节数参与字节阈值判定。
//   - 使用 >= 而非 >，因此在阈值边界（恰好相等）即触发；负的计数值不会误触发，
//     整个判定为常数时间、无循环、无分配，输入再大也不会越界。
//
// 接线说明：jobs 的日志由子进程直接写入 fd（child.Stdout/Stderr），registry 索引
// 用临时文件 + 原子替换持久化，两者都没有"每次 Write 追加"的写入器，故本函数
// 暂无可接线的位置，仅作为纯判定能力交付。候选接线点见提交报告。
func shouldFlushBatch(count int, bytes int, elapsed, maxDelay time.Duration, maxCount, maxBytes int) bool {
	if maxCount > 0 && count >= maxCount {
		return true
	}
	if maxBytes > 0 && bytes >= maxBytes {
		return true
	}
	if maxDelay > 0 && elapsed >= maxDelay {
		return true
	}
	return false
}
