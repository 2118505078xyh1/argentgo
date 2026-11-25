package extension

import (
	"fmt"

	"example.com/argentgo/internal/hooks"
)

// trustAuthorizer 把 hooks.TrustStore 的 (key,digest) 授权包装成门面的 Authorizer。
// 它复用 hooks 已验证的 fail-closed 语义：摘要不在存储中即拒绝，不做任何路径豁免。
type trustAuthorizer struct {
	trust *hooks.TrustStore
}

// NewAuthorizer 构造信任门。trust 为 nil 时返回 Enabled=false 的实现——
// 这表示"该体系无信任概念"，调用方应据此跳过授权，而非视为已授信。
func NewAuthorizer(trust *hooks.TrustStore) Authorizer {
	if trust == nil {
		return NoopAuthorizer{}
	}
	return &trustAuthorizer{trust: trust}
}

func (a *trustAuthorizer) Enabled() bool { return true }

func (a *trustAuthorizer) Authorize(key, digest string) error {
	if a.trust.IsTrusted(key, digest) {
		return nil
	}
	return fmt.Errorf("%w: key %q digest %s", hooks.ErrUntrusted, key, digest)
}

// NoopAuthorizer 是无信任概念体系的门面实现。Enabled 为 false，
// Authorize 恒成功——但调用方应先看 Enabled，不要把它当作安全门。
type NoopAuthorizer struct{}

func (NoopAuthorizer) Enabled() bool                  { return false }
func (NoopAuthorizer) Authorize(string, string) error { return nil }
