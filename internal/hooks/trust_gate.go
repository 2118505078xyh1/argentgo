package hooks

// needsTrustGate 报告一份钩子文档是否通过了摘要授信门控。
//
// 所有钩子文档都必须先通过摘要授权才能运行——包括用户全局路径下的文档，
// 不存在按路径（项目内/全局）区分的豁免。这与 LoadDocument、Source 以及
// reload 等真实加载路径的既有行为一致：它们一律对文档摘要调用
// TrustStore.IsTrusted，未授信即返回 ErrUntrusted。
//
// 旧的基于项目根路径的实现已删除：它从未被生产代码调用，其"全局路径豁免"
// 注释也与真实行为不符。trust 为 nil 时没有任何文档被授信；
// digest 应由 Digest(data) 计算得出。
func needsTrustGate(path, digest string, trust *TrustStore) bool {
	return trust != nil && trust.IsTrusted(path, digest)
}
