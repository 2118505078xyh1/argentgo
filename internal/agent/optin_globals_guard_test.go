package agent

import (
	"errors"
	"sync"
	"testing"
)

// TestOptInSettersConcurrentSafeAndRaceFree 是 R28 并发审计的守卫网。
//
// 进程级 opt-in 开关的 setter 会在装配点与测试之间被反复读写。此前只有
// SetToolErrorReflection 具备并发安全测试，其余开关靠人工 defer 兜底，缺乏
// 统一的竞争守卫。这里对 agent 包内可访问的每一个开关：启动 writer goroutine
// 反复 Set(true/false)，同时启动 reader goroutine 反复调用对应 getter 与读取
// 全局的消费纯函数（classifyRefusal、AugmentToolErrorReflection、canRunParallel、nudgePayload）。
//
// 所有开关底层都是 atomic.Bool / atomic.Value，因此在 -race 下应当无数据竞争；
// 若有人把开关退回普通 bool 或就地修改已发布的 map，这里会立刻捕获。
//
// 刻意不调用 t.Parallel()：这些是进程级全局状态，必须与同包其它测试串行，
// 否则会与依赖默认关闭状态的测试相互污染。
func TestOptInSettersConcurrentSafeAndRaceFree(t *testing.T) {
	// 用 Cleanup 恢复默认：11 个 bool 开关默认 false，并发安全白名单默认是
	// init 中发布的一个空 map（用 SetParallelSafeTools(nil) 还原）。
	t.Cleanup(func() {
		SetRefusalReason(false)
		SetToolErrorReflection(false)
		SetToolResultWrapping(false)
		SetOutputRedaction(false)
		SetToolArgumentCoercion(false)
		SetToolNameRecovery(false)
		SetToolOutputConfirmation(false)
		SetParallelDeclaredOnly(false)
		SetPartialRecordPersistence(false)
		SetGuidanceDedupe(false)
		SetRetainGuidance(false)
		SetParallelSafeTools(nil)
	})

	// 消费纯函数使用固定入参，避免迭代中分配；普通错误不会命中任何拒绝/修复
	// 标记，仅用于驱动"读取全局开关后走最短路径"的分支。
	refusalErr := errors.New("some tool failure")
	reflectOutput := "error: permission denied"

	// optInCase 把一个开关的写端、读端与消费端绑在一起。
	type optInCase struct {
		name    string
		set     func(bool)
		get     func() bool
		consume func() // 读取该全局开关的消费纯函数；没有独立纯消费者时为 nil
	}
	cases := []optInCase{
		{
			name: "refusal_reason",
			set:  SetRefusalReason,
			get:  refusalReasonEnabled,
			// classifyRefusal 在打开时读取 refusalReason 全局再调用 security.ReasonFromError。
			consume: func() { _ = classifyRefusal(refusalErr) },
		},
		{
			name: "tool_error_reflection",
			set:  SetToolErrorReflection,
			get:  toolErrorReflectionEnabled,
			// AugmentToolErrorReflection 在 isError 时读取 toolErrorReflection 全局，
			// 再经过 reflectionWorthy / toolErrorReflection 等纯函数。
			consume: func() { _ = AugmentToolErrorReflection("edit", reflectOutput, true) },
		},
		{
			name: "tool_result_wrapping",
			set:  SetToolResultWrapping,
			get:  toolResultWrappingEnabled,
		},
		{
			name: "output_redaction",
			set:  SetOutputRedaction,
			get:  outputRedactionEnabled,
			// appendTool 在 outputRedactionEnabled() 为真时读取全局并调用 security 脱敏，
			// 这里直接以最小 runState 驱动这条消费分支。
			consume: func() {
				s := &runState{}
				s.appendTool(toolCall("c", "read", `{}`), "sk-abcdef1234567890\nDB_PASSWORD=myP@ss!\n")
			},
		},
		{
			name: "tool_argument_coercion",
			set:  SetToolArgumentCoercion,
			get:  toolArgumentCoercionEnabled,
		},
		{
			name: "tool_name_recovery",
			set:  SetToolNameRecovery,
			get:  toolNameRecoveryEnabled,
		},
		{
			name: "tool_output_confirmation",
			set:  SetToolOutputConfirmation,
			get:  toolOutputConfirmationEnabled,
		},
		{
			name: "parallel_declared_only",
			set:  SetParallelDeclaredOnly,
			get:  parallelDeclaredOnlyEnabled,
		},
		{
			name: "partial_record_persistence",
			set:  SetPartialRecordPersistence,
			get:  partialRecordPersistenceEnabled,
		},
		{
			name: "guidance_dedupe",
			set:  SetGuidanceDedupe,
			get:  guidanceDedupeEnabled,
		},
		{
			name: "retain_guidance",
			set:  SetRetainGuidance,
			get:  retainGuidanceEnabled,
			// nudgePayload 读取 retainGuidance 全局：关闭时返回原始字符串，
			// 开启时返回带 _retention 的 retainedGuidance 对象。这里只调用以驱动
			// "读全局后分支"的路径；开关开/关的返回值差异由
			// guidance_retention_test.go 的基线/保留测试逐字节守护。
			consume: func() { _ = nudgePayload("guard 提示") },
		},
		{
			// SetParallelSafeTools 不是 bool 开关，而是整体替换只读 map 的
			// atomic.Value。这里用 set(true)=写入白名单、set(false)=清空白名单
			// 适配同一张表；读端通过 declaredParallelSafe() 读取，消费纯函数
			// 用 canRunParallel（只依据传入的快照判定，不读全局）。
			name: "parallel_safe_tools",
			set: func(on bool) {
				if on {
					SetParallelSafeTools([]string{"read", "edit"})
				} else {
					SetParallelSafeTools(nil)
				}
			},
			get: func() bool {
				return len(declaredParallelSafe()) >= 0
			},
			consume: func() { _ = canRunParallel("read", declaredParallelSafe()) },
		},
	}

	const (
		writers = 4
		readers = 4
		rounds  = 200
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := make(chan struct{})
			var wg sync.WaitGroup

			// writer：反复切换开关。不同 goroutine 写入相反的值，制造真实竞争窗口。
			for w := 0; w < writers; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					<-start
					for i := 0; i < rounds; i++ {
						c.set((i+w)%2 == 0)
					}
				}(w)
			}

			// reader：反复读取 getter 与消费纯函数；读到的值是什么都接受，
			// 关键是 -race 下不能出现数据竞争。
			for r := 0; r < readers; r++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					for i := 0; i < rounds; i++ {
						_ = c.get()
						if c.consume != nil {
							c.consume()
						}
					}
				}()
			}

			close(start)
			wg.Wait()
		})
	}
}
