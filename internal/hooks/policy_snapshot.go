package hooks

// 委派权限快照中的键。source 标记用来区分"由父会话派生的只读快照"与调用方
// 直接构造的普通权限映射。标记键不在复制白名单内，因此父级自带的 source 永不
// 被复制，快照的标记始终由本包权威给出。
const (
	policyKeySandbox      = "sandbox"
	policyKeyApproval     = "approval"
	policyKeyPreset       = "preset"
	policyKeySource       = "source"
	policySourceDelegated = "delegation"
)

// delegatedPolicyKeys 是会被带进委派快照的权限相关键。顺序固定，使快照的
// 构造不依赖 Go 的随机 map 迭代顺序（即输出稳定、可复现）。
var delegatedPolicyKeys = [...]string{
	policyKeySandbox,
	policyKeyApproval,
	policyKeyPreset,
}

// delegatedPolicySnapshot 为子代理委派生成父会话权限的只读快照。
//
// 它只复制白名单内的权限相关键（sandbox / approval / preset），并写入
// source=delegation 标记。子级拿到的是父级权限在某个时间点的固定副本，
// 因而无法在委派链中通过改写权限映射来提权。函数具备以下性质：
//
//   - nil 安全：parent 为 nil 时返回仅含标记的快照，不 panic；
//   - 不改入参：只读 parent，返回全新映射，与其不共享可变状态；
//   - CJK 安全：值按字符串原样复制，不做任何字节切片或截断；
//   - 稳定：按固定的键顺序构造，同样的输入总得到内容一致的快照。
//
// 父级中不在白名单的键一律被丢弃，因此凭据、提示词等无关内容不会随委派扩散。
func delegatedPolicySnapshot(parent map[string]string) map[string]string {
	snapshot := make(map[string]string, len(delegatedPolicyKeys)+1)
	for _, key := range delegatedPolicyKeys {
		if value, ok := parent[key]; ok {
			snapshot[key] = value
		}
	}
	// 标记永远由快照自身给出；parent 即便带有 source 键也不会被复制到这里。
	snapshot[policyKeySource] = policySourceDelegated
	return snapshot
}

// DelegatedPolicySnapshot 是委派权限快照的导出接线缝，供调用方（internal/app）
// 在构建子运行器时以 opt-in 方式使用：默认不调用，现有委派行为完全不变。
// 它只是 delegatedPolicySnapshot 的薄封装，语义一致。
func DelegatedPolicySnapshot(parent map[string]string) map[string]string {
	return delegatedPolicySnapshot(parent)
}
