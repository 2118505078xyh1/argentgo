package proc

// shouldKillTreeAfterExit 决定取消子进程时，在直接子进程退出之后是否仍要
// 升级为 killTree（SIGKILL 整棵进程树）。
//
// 背景：两阶段取消先向整棵树发 SIGTERM（优雅停止），再等待一个宽限期。
// 旧逻辑一旦看到直接子进程退出就立即返回，跳过 killTree；但忽略 SIGTERM 的
// 孙进程可能仍存活，于是变成脱离管控的孤儿进程。本函数把"是否仍需补刀"
// 的判断沉淀为纯决策，供取消路径复用，避免各处重新推导这条判断链。
//
// 只有在确实发过 SIGTERM（sigTermSent 为真）时，两阶段取消才成立；否则
// 没有任何优雅停止发生，直接子进程的退出只是普通结局，不应因此去杀树。
// 在 sigTermSent 为真时，以下任一情形都要求 killTree：
//
//   - graceExpired：宽限期耗尽而 shutdown 未完成，按既定语义升级为 SIGKILL；
//   - directExited：直接子进程在宽限内退出，但孙进程可能忽略了 SIGTERM，
//     必须补杀整棵树，防止孙进程残留（审计发现的缺口正在于此）。
//
// 参数与八种组合（sigTermSent, graceExpired, directExited）：
//
//   - (false, _, _)            一律 false：未发 SIGTERM，没有两阶段取消，
//     杀树决策不适用；
//   - (true, false, false)     false：已发 SIGTERM，仍在宽限期内且子进程
//     未退出，继续等待，不提前杀树；
//   - (true, false, true)      true：已发 SIGTERM，直接子进程在宽限内退出，
//     仍需 killTree 清理可能忽略 SIGTERM 的孙进程（关键修复场景）；
//   - (true, true, false)      true：宽限耗尽、子进程仍不退出，升级为杀树；
//   - (true, true, true)       true：宽限耗尽且子进程已退出，仍执行杀树
//     （对已退出的进程发 SIGKILL 是无害的空操作，却能覆盖存活的孙进程）。
//
// 这是纯函数：只消费三个布尔值，不读取全局状态、不做任何 I/O、不解码任何
// 文本，因而与 CJK / 多字节编码无关；相同输入永远得到相同输出，可被并发
// 调用而无需同步。已在 Run / cancelChildGracefully 的取消路径接线：直接子进程
// 在宽限期内退出时仍补刀杀树，清理忽略 SIGTERM 的孤儿童进程。
func shouldKillTreeAfterExit(sigTermSent, graceExpired, directExited bool) bool {
	return sigTermSent && (graceExpired || directExited)
}
