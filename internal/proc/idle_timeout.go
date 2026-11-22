package proc

import "errors"

// ErrIdleTimeout 表示子进程因两次输出之间的静默超过 IdleTimeout 而被终止。
// 调用方可用 errors.Is 匹配，区分整体超时（DeadlineExceeded）与卡死。
var ErrIdleTimeout = errors.New("proc: idle timeout")
