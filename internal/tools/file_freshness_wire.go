package tools

import "context"

// fileFreshnessCtxKey 是携带本次会话文件新鲜度账本的 context key。
// 私有类型身份确保只有 tools 包自己能读写；app 通过 WithFileFreshness 注入。
type fileFreshnessCtxKey struct{}

// WithFileFreshness 把新鲜度账本放进 ctx。f 为 nil 时相当于不注入。
func WithFileFreshness(ctx context.Context, f *FileFreshness) context.Context {
	if ctx == nil || f == nil {
		return ctx
	}
	return context.WithValue(ctx, fileFreshnessCtxKey{}, f)
}

// FileFreshnessFrom 从 ctx 取出账本；未注入时返回 nil，调用方直接跳过所有检查。
func FileFreshnessFrom(ctx context.Context) *FileFreshness {
	if ctx == nil {
		return nil
	}
	f, _ := ctx.Value(fileFreshnessCtxKey{}).(*FileFreshness)
	return f
}
