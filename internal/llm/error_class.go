package llm

// FailureKind 是向调用方暴露的失败类别的封闭词汇表。
// 字符串值是稳定的，因此前端和会话层无需导入本包类型即可
// 持久化或比较它们。
//
// 它刻意比 ActionableError.Family 更粗：若干提供方
// 家族（network、server）归为 "transient"，而提供方侧的
// 内容拦截是 "agent_action"，而非传输失败。
type FailureKind string

const (
	FailureAuth            FailureKind = "auth"
	FailureQuota           FailureKind = "quota"
	FailureRateLimit       FailureKind = "rate_limit"
	FailureConfig          FailureKind = "config"
	FailureTransient       FailureKind = "transient"
	FailureAgentAction     FailureKind = "agent_action"
	FailureContextOverflow FailureKind = "context_overflow"
	// FailureInternal 保留给本进程内部的故障。ClassifyFailure
	// 目前不会发出它；它存在是为了让词汇表封闭，并让未来的
	// 内部故障信号无需改动类型即可路由。
	FailureInternal FailureKind = "internal"
	FailureUnknown  FailureKind = "unknown"
)

// UserAction 是失败后调用方应提供的处置方式。它是
// 三值路由提示，而非 UI 文案：
//
// none     用户无需做任何事；报告失败或在内部处理
// retry    用户可以重试（等待或修改请求后）
// settings 用户必须先修复凭据、模型或其他设置
type UserAction string

const (
	UserActionNone     UserAction = "none"
	UserActionRetry    UserAction = "retry"
	UserActionSettings UserAction = "settings"
)

// FailureInfo 是对失败调用的脱敏分类。它刻意
// 没有 message、body 或 detail 字段：ErrorID 是从表中选取的固定标识符，
// 因此可以安全地记录、持久化和发送给前端。
type FailureInfo struct {
	Kind       FailureKind
	Retryable  bool
	UserAction UserAction
	ErrorID    string
}

// failureByFamily 把 ActionableError 家族转换为封闭的
// 词汇表。把转换放在这里意味着不重复任何哨兵或正则逻辑：
// ClassifyAPIError 仍是"发生了什么"的唯一事实来源，
// 包括其权威异常优先的顺序，而这张
// 表只决定类别、可重试性、用户处置方式和简短 id。
//
// Retryable 回答"不做改动的重试会自行成功吗？"，而
// UserAction 回答"用户该做什么？"。两者可能不同：内容
// 拦截不能安全地原样重放（Retryable=false），但仍
// 引导用户修改提示后重试（UserAction=retry）。上下文
// 溢出也是 Retryable=false（同样的超大请求会再次
// 失败），并被路由到设置，让调用方触发压缩。
var failureByFamily = map[string]FailureInfo{
	FamilyNotConfigured:   {FailureConfig, false, UserActionSettings, "config.not_configured"},
	FamilyAuth:            {FailureAuth, false, UserActionSettings, "auth.invalid_key"},
	FamilyQuotaExhausted:  {FailureQuota, false, UserActionSettings, "quota.exhausted"},
	FamilyRateLimit:       {FailureRateLimit, true, UserActionRetry, "rate_limit.exceeded"},
	FamilyContentFilter:   {FailureAgentAction, false, UserActionRetry, "agent_action.content_filter"},
	FamilyContextOverflow: {FailureContextOverflow, false, UserActionSettings, "context_overflow.exceeded"},
	FamilyModelNotFound:   {FailureConfig, false, UserActionSettings, "config.model_not_found"},
	FamilyNetwork:         {FailureTransient, true, UserActionRetry, "transient.network"},
	FamilyServer:          {FailureTransient, true, UserActionRetry, "transient.server"},
	FamilyUnknown:         {FailureUnknown, false, UserActionNone, "unknown"},
}

// unknownFailure 是 nil 错误或本构建不认识的家族的回退
// （例如由更新调用方构造的 ActionableError）。
// 无法识别的失败绝不会被假定为可重试，也不会对用户
// 提出任何要求，因此不会静默重放请求，也不会捏造细节。
var unknownFailure = FailureInfo{FailureUnknown, false, UserActionNone, "unknown"}

// ClassifyFailure 把任意错误映射到封闭的失败词汇表。
//
// 它复用 ClassifyAPIError，因此在转换家族之前，权威异常类别（net/context
// 哨兵、包装的流错误）会胜过通用文本模式。结果不含提供方文本或凭据：
// ErrorID 始终是 failureByFamily 中的固定标识符之一。
// ErrorID is always one of the fixed identifiers in failureByFamily.
//
// nil 被报告为 unknown/none 且不含细节；把"无错误"当作
// 特殊情况的调用方，可以在调用前继续检查 err。
func ClassifyFailure(err error) FailureInfo {
	if err == nil {
		return unknownFailure
	}
	if info, ok := failureByFamily[ClassifyAPIError(err).Family]; ok {
		return info
	}
	return unknownFailure
}
