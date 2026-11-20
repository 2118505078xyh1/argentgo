package jobs

// lockOwnerInfo 生成数据目录排他锁文件的拥有者元数据，用于在诊断时回答
// “哪一个进程持有这把锁”。它是纯函数：不触碰文件系统，也不读取当前进程的
// 状态，只把调用方给定的三个值填入一个字段固定的 map，便于单独测试与复用。
//
// 返回的键名固定且稳定，便于任何语言或工具读取：
//   - pid：持有锁的进程 id；
//   - version：持有锁的程序版本；
//   - started_at：持有锁的进程的启动时间（Unix 秒）。
//
// version 按 UTF-8 字节原样保存，CJK 字符不做 ASCII 转写，因此对中英文
// 版本都稳定；零值与负值也原样记录 —— 这是诊断信息，函数不替调用方裁剪
// 含义（例如 started_at 为 0 通常表示“启动时间未知”）。
//
// 同一组输入始终得到相同的键值；由于标准库 json.Marshal 会按字典序输出
// map 的键，序列化结果也是稳定的。字段类型固定为 int / string / int64。
func lockOwnerInfo(pid int, version string, startedAt int64) map[string]any {
	return map[string]any{
		"pid":        pid,
		"version":    version,
		"started_at": startedAt,
	}
}
