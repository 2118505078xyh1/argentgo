package proc

// rollbackOffset 计算追加失败后字节级回滚应截断到的文件偏移。
//
// 目标偏移恒为 beforeSize（本次追加开始之前的文件字节大小）：把文件截断回
// 这个偏移，就能完整移除失败写入留下的半行残尾，且不会触碰追加前已有的任何
// 字节。计算只消费两个整数，不解码任何文本，因此与编码（CJK / 多字节
// UTF-8）及行边界无关；结果始终落在 [0, beforeSize] 内、有界，且不含循环。
//
// 参数：
//   - beforeSize：追加开始之前的文件大小，也是回滚的目标偏移；
//   - writtenBytes：fd.Write 在失败前报告的已写出字节数，用来描述是否真的
//     有新字节落盘。
//
// 边界与保护：
//   - writtenBytes < 0 没有物理意义，按 0 处理；
//   - writtenBytes > 0 表示存在需要清除的残尾，返回 beforeSize 供调用方截断；
//   - writtenBytes == 0 表示没有新字节落盘，截断回 beforeSize 是无害的
//     no-op。两种情况返回同一偏移，让调用方无需分支即可安全回滚；
//   - beforeSize < 0 属于非法输入（os.Truncate 拒绝负偏移），夹到 0，
//     保证返回值永不为负、有界。
//
// 这是纯函数：不读取全局状态，不做任何 I/O，可在表驱动测试中直接调用，
// 也可在将来由会话追加路径 opt-in 调用而不改变当前任何字节级行为
// （当前尚未接线，见接线点报告）。
func rollbackOffset(beforeSize, writtenBytes int64) int64 {
	if writtenBytes < 0 {
		writtenBytes = 0
	}
	if beforeSize < 0 {
		return 0
	}
	if writtenBytes == 0 {
		// 没有新字节落盘：截断是 no-op，仍返回合法偏移以保持接口为全函数。
		return beforeSize
	}
	return beforeSize
}
