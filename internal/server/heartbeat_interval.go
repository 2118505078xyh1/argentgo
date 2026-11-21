package server

import "time"

// 本文件把「SSE 心跳间隔」收敛成一个纯函数，使间隔可通过 Config 配置，
// 同时保留固定的默认值与安全上限。
//
// 背景：反向代理通常会在连接空闲超过一定时间后将其断开。SSE 空闲流靠
// 周期性心跳注释保活；间隔太短浪费带宽，太长则挡不住代理超时。因此需要
// 允许部署方按代理的空闲超时来配置，并给出默认值与上限兜底。

const (
	// maxHeartbeatInterval 是心跳间隔的上限（60 秒）。超过上限的配置会被
	// 钳制而不是回退默认值：调用方明显想要「尽可能久」，钳制更贴近意图，
	// 同时保证至少每 60 秒有一次保活流量，不被常见代理的空闲超时掐断。
	maxHeartbeatInterval = 60 * time.Second
)

// heartbeatInterval 把调用方提供的心跳间隔归一化为安全可用的值：
//
//   - 非正数（零、负值、time.Duration 的最小值）回退到默认间隔
//     eventHeartbeatInterval（15 秒）；
//   - 超过 maxHeartbeatInterval 的正数（含 time.Duration 的最大值）
//     钳制到上限；
//   - 其余正数按原样返回，包括 (0, 15s) 的小间隔（测试用短心跳即依赖此点）。
//
// 边界稳定：恰好等于默认值或上限时原样返回边界值，不抖动；函数确定且幂等，
// 重复施加不改变结果。该函数只做数值比较，不读时钟、不执行 IO、不触碰全局
// 状态，也不做任何字符串处理，因此与 CJK 字符及区域设置无关。
func heartbeatInterval(provided time.Duration) time.Duration {
	if provided <= 0 {
		return eventHeartbeatInterval
	}
	if provided > maxHeartbeatInterval {
		return maxHeartbeatInterval
	}
	return provided
}
