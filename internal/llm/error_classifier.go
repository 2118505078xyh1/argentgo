package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"regexp"
	"strings"
)

// ErrorClass 是对错误做恢复决策时使用的粗粒度分级。它刻意只回答
// "这一失败属于哪一类"，不携带任何提供方文本或凭据，因此可被安全地
// 比较、持久化或传给前端。字符串值稳定，调用方无需导入本包类型即可
// 比较。
//
// 它与 ClassifyFailure 使用的 FailureKind 刻意不同：后者面向"用户该
// 做什么"，这里面向"重试/回退策略如何处置"，因此把 502/503 归入
// transient（而非笼统的 server），并把 400/invalid request 单列一类。
type ErrorClass string

// 分类常量。小写名称是任务约定的规范名；导出别名让本包之外的
// 调用方（例如未来的重试/回退接线层）也能比较 ClassifyErrorClass 的结果。
const (
	classTransient      ErrorClass = "transient"
	classAuth           ErrorClass = "auth"
	classRateLimit      ErrorClass = "rate_limit"
	classInvalidRequest ErrorClass = "invalid_request"
	classServer         ErrorClass = "server"
	classUnknown        ErrorClass = "unknown"
)

// ErrorClass 的导出别名，供本包之外的调用方比较结果。
const (
	ErrorClassTransient      = classTransient
	ErrorClassAuth           = classAuth
	ErrorClassRateLimit      = classRateLimit
	ErrorClassInvalidRequest = classInvalidRequest
	ErrorClassServer         = classServer
	ErrorClassUnknown        = classUnknown
)

// 各类关键字模式。全部按大小写不敏感编译，并同时覆盖英文与简体中文
// 文案。状态码用 \b 界定，避免 "1400"、"4290" 之类的数字误命中。
//
// 拆分而非合并为一条大正则，是为了让 precedence（下文的 switch）显式：
// 同一错误同时带有多个信号时，更具体的类别先赢。模式在包级只编译一次，
// ClassifyErrorClass 因此是无状态且确定性的纯函数。
var (
	errorClassRateLimitRE = regexp.MustCompile(`(?i)(\b429\b|too[_\s-]?many[_\s-]?requests|rate[_\s-]?limit|throttl|请求过于频繁|请求太频繁|频率限制|限流)`)

	errorClassAuthRE = regexp.MustCompile(`(?i)(\b401\b|\b403\b|unauthori[sz]ed|invalid[_\s-]?api[_\s-]?key|invalid[_\s-]?token|expired[_\s-]?token|authentication[_\s-]?(?:failed|error|required)|forbidden|login[_\s-]?required|invalid[_\s-]?credentials?|api[_\s-]?key[^\n]{0,24}?(?:invalid|incorrect|wrong|expired|revoked)|认证失败|鉴权失败|未授权|无权访问|禁止访问|密钥无效|密钥已失效|密钥过期|登录已过期)`)

	// transient 同时覆盖明确的瞬时网络故障与 502/503/504 这类"上游暂不可用"
	// 的状态码。它们优先于通用的 5xx→server，因为原样重试通常能成功。
	errorClassTransientRE = regexp.MustCompile(`(?i)(\b502\b|\b503\b|\b504\b|timeout|timed[_\s-]?out|deadline[_\s-]?exceeded|connection[_\s-]?reset|connection[_\s-]?refused|connection[_\s-]?closed|closed[_\s-]?network[_\s-]?connection|temporarily[_\s-]?unavailable|超时|连接重置|连接被重置|连接被关闭|连接被拒绝|网络中断|网络超时|请求超时|暂时不可用)`)

	errorClassInvalidRequestRE = regexp.MustCompile(`(?i)(\b400\b|invalid[_\s-]?request|bad[_\s-]?request|malformed[_\s-]?request|无效请求|请求无效|错误请求|请求参数错误)`)

	errorClassServerRE = regexp.MustCompile(`(?i)(\b5\d\d\b|internal[_\s-]?server[_\s-]?error|service[_\s-]?unavailable|server[_\s-]?error|服务器错误|服务器内部错误|服务不可用|内部服务器错误|服务暂时不可用|系统繁忙)`)
)

// maxErrorChainDepth 限制遍历包装链的深度，防止病态的自引用错误导致死循环。
const maxErrorChainDepth = 64

// ClassifyErrorClass 把任意错误映射到恢复策略使用的粗粒度 ErrorClass。
//
// 注意：本包 attempt.go 已有同名的 ClassifyError（返回 AttemptKind），
// 由于 Go 不支持函数重载且本次改动被限制在新文件内，故此处命名为
// ClassifyErrorClass；若需在外部以 ClassifyError 暴露，需先重命名 attempt.go
// 中的同名函数。
//
// 匹配规则（按优先级从高到低，大小写不敏感，支持简体中文）：
//   - rate limit / 429          → classRateLimit
//   - 401 / 403 / unauthorized / invalid api key → classAuth
//   - timeout / connection reset / 502 / 503 → classTransient
//   - 400 / invalid request     → classInvalidRequest
//   - 其余 5xx                  → classServer
//   - 其余                      → classUnknown
//
// 它遍历错误的 %w 包装链并拼接各层文本，因此对 errors.Is/errors.As 友好的
// 包装错误仍能被正确分类；若文本无命中，再用已知哨兵和标准库网络错误兜底。
// 这是纯函数：不修改入参、不读写共享状态，nil 返回 classUnknown。
func ClassifyErrorClass(err error) ErrorClass {
	if err == nil {
		return classUnknown
	}
	if class := classifyErrorClassText(errorChainText(err)); class != classUnknown {
		return class
	}
	// 文本无关键字时，结构化信号兜底：先状态码，再标准库哨兵。
	if class := classifyErrorStatus(err); class != classUnknown {
		return class
	}
	if isTransientSentinel(err) {
		return classTransient
	}
	return classUnknown
}

// classifyErrorClassText 按固定优先级在一段错误文本上选择类别。
func classifyErrorClassText(text string) ErrorClass {
	switch {
	case errorClassRateLimitRE.MatchString(text):
		return classRateLimit
	case errorClassAuthRE.MatchString(text):
		return classAuth
	case errorClassTransientRE.MatchString(text):
		return classTransient
	case errorClassInvalidRequestRE.MatchString(text):
		return classInvalidRequest
	case errorClassServerRE.MatchString(text):
		return classServer
	default:
		return classUnknown
	}
}

// errorChainText 把错误及其 %w 包装链的各层 Error() 文本用换行拼接，
// 使外层添加上下文后，内层的分类关键字依然可见。
func errorChainText(err error) string {
	var b strings.Builder
	for depth := 0; err != nil && depth < maxErrorChainDepth; depth++ {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(err.Error())
		err = errors.Unwrap(err)
	}
	return b.String()
}

// errorStatusCoder 是常见的结构化错误契约：若错误（或其包装链中的某一层）
// 暴露 HTTP 状态码，可在文本无关键字时据此分类。
type errorStatusCoder interface {
	StatusCode() int
}

// classifyErrorStatus 用 errors.As 在包装链中查找状态码，再映射到类别。
func classifyErrorStatus(err error) ErrorClass {
	var sc errorStatusCoder
	if !errors.As(err, &sc) {
		return classUnknown
	}
	return classFromStatusCode(sc.StatusCode())
}

// classFromStatusCode 把 HTTP 状态码映射到类别。未列出的码返回 classUnknown，
// 由调用方继续走其它兜底。
func classFromStatusCode(code int) ErrorClass {
	switch code {
	case 429:
		return classRateLimit
	case 401, 403:
		return classAuth
	case 502, 503, 504:
		return classTransient
	case 400:
		return classInvalidRequest
	}
	if code >= 500 && code <= 599 {
		return classServer
	}
	return classUnknown
}

// isTransientSentinel 识别标准库中表示瞬时/传输故障的哨兵错误。
// errors.Is 会穿过 %w 包装链，因此包装后的哨兵同样命中。
func isTransientSentinel(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}
