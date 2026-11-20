package auth

import (
	"errors"
	"time"
)

// DevicePollState 是在一次令牌端点回答之后设备码轮询循环进入的
// DevicePollState 是在一次令牌端点回答之后设备码轮询循环进入的状态。它使 RFC 8628 轮询状态机对调用方可见，而非隐藏在 DevicePoll 的循环内部。
// 对调用方可见，而非隐藏在 DevicePoll 循环内部。
type DevicePollState string

// ClassifyDevicePoll 报告的 DevicePollState 值。
const (
	// DevicePollAuthorized 表示端点返回了令牌；轮询结束。
	DevicePollAuthorized DevicePollState = "authorized"
	// DevicePollWaiting 表示用户尚未完成；在 Wait 后再次轮询。
	DevicePollWaiting DevicePollState = "waiting"
	// DevicePollExpired 表示设备码（或轮询窗口）已用尽。
	DevicePollExpired DevicePollState = "expired"
	// DevicePollDenied 表示用户拒绝了授权。
	DevicePollDenied DevicePollState = "denied"
	// DevicePollFailed 表示意外响应；停止而不重试。
	DevicePollFailed DevicePollState = "failed"
)

// String 实现 fmt.Stringer。
func (s DevicePollState) String() string { return string(s) }

// DevicePollDecision 是设备码轮询状态机的一步。
// Wait 是下次轮询前的延迟，仅当 State 为 DevicePollWaiting 时有意义；对所有终止状态它为零。
// DevicePollWaiting；对所有终止状态它为零。
type DevicePollDecision struct {
	State DevicePollState
	Wait  time.Duration
}

// ClassifyDevicePoll 把一个令牌端点结果映射为下一个
// ClassifyDevicePoll 把一个令牌端点结果映射为下一个轮询决定。它是纯函数：无 I/O、无时钟、无睡眠，因此调用方能在测试中驱动状态机，或独立于
// 在测试中驱动状态机，或独立于轮询循环重用分类。
// DevicePoll.
//
// nil err 表示成功（响应已携带令牌）。code 为 authorization_pending 的 tokenError 使循环以相同间隔继续等待；
// code 为 authorization_pending 的 tokenError 使循环以相同间隔等待；
// slow_down 也使其等待但把间隔延长
// slow_down 也使其等待但把间隔延长 deviceSlowDownStep；expired_token 和 access_denied 是终止状态。任何其他
// 错误（包括非 tokenError 的传输失败）都是使循环停止的失败。提供的间隔原样透传，除
// 循环。提供的间隔原样透传，除 slow_down 外，
// slow_down 外，因此调用方能把上一个决定的 Wait 反馈回来。
func ClassifyDevicePoll(err error, interval time.Duration) DevicePollDecision {
	if err == nil {
		return DevicePollDecision{State: DevicePollAuthorized}
	}
	var failure *tokenError
	if !errors.As(err, &failure) {
		return DevicePollDecision{State: DevicePollFailed}
	}
	switch failure.Code {
	case "authorization_pending":
		return DevicePollDecision{State: DevicePollWaiting, Wait: interval}
	case "slow_down":
		return DevicePollDecision{State: DevicePollWaiting, Wait: interval + deviceSlowDownStep}
	case "expired_token":
		return DevicePollDecision{State: DevicePollExpired}
	case "access_denied":
		return DevicePollDecision{State: DevicePollDenied}
	default:
		return DevicePollDecision{State: DevicePollFailed}
	}
}
