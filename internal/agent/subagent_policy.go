package agent

import "sort"

// 默认被拒绝、不允许子代理继承的工具名。
//
// task / todowrite 允许在子代理显式申请时被豁免（见 SpawnPolicy.ChildAllow），
// 而 spawn 永不豁免：防止子代理再派生下一级子代理，从而逐层放大权限。
const (
	childDenyTask      = "task"
	childDenyTodoWrite = "todowrite"
	childDenySpawn     = "spawn"
)

// DenySet 是父级已经拒绝的工具名集合，作为权限投影的只读输入。
//
// 使用 map 作为集合以便 O(1) 查询；DeriveChildPolicy 只读取它，
// 不会修改调用方持有的这份集合。
type DenySet map[string]struct{}

// SpawnPolicy 描述派生子代理时的权限约束。
type SpawnPolicy struct {
	// DenyTools 是本次 spawn 额外追加的拒绝工具名。
	// 子级只能进一步收缩权限，因此这些拒绝永远不能被 ChildAllow 豁免。
	DenyTools []string
	// ChildAllow 是子代理显式申请豁免的工具名。
	// 只有 task / todowrite 这两个默认拒绝项可以被豁免，spawn 与其他拒绝项不受影响。
	ChildAllow []string
}

// ChildPolicy 是 DeriveChildPolicy 派生出的子代理权限投影。
//
// Allow 是被实际授予豁免的工具名（排序稳定）；
// Deny 是最终生效的拒绝工具名（排序稳定）。
type ChildPolicy struct {
	Allow []string
	Deny  []string
}

// DeriveChildPolicy 把父级的拒绝集合投影为子代理的权限。
//
// 规则（按优先级）：
//  1. 继承父级 deny，并叠加 child.DenyTools；这两者属于"硬拒绝"，不可豁免。
//  2. 强制追加默认拒绝 task / todowrite / spawn。
//  3. child.ChildAllow 仅能豁免 task / todowrite，且仅当该工具没有被硬拒绝；
//     spawn 永不豁免，以防嵌套越权。
//
// 本函数是纯函数：不修改 parent、child.DenyTools、child.ChildAllow；
// 输出集合先去重再排序，因此结果是确定性的。所有比较都基于完整的 UTF-8 字符串，
// 不做字节切片或单字节截断，因而对 CJK 工具名同样安全。
func DeriveChildPolicy(parent DenySet, child SpawnPolicy) ChildPolicy {
	// 可被 ChildAllow 豁免的默认拒绝项：不含 spawn。
	exemptable := map[string]struct{}{
		childDenyTask:      {},
		childDenyTodoWrite: {},
	}

	// 硬拒绝：父级 deny + 本次追加的 deny + spawn。
	deny := make(map[string]struct{}, len(parent)+len(child.DenyTools)+1)
	for name := range parent {
		deny[name] = struct{}{}
	}
	for _, name := range child.DenyTools {
		deny[name] = struct{}{}
	}
	deny[childDenySpawn] = struct{}{}

	// 实际授予的豁免：仅默认拒绝项、且未被硬拒绝。
	allow := make(map[string]struct{}, len(child.ChildAllow))
	for _, name := range child.ChildAllow {
		if _, ok := exemptable[name]; !ok {
			continue
		}
		if _, hard := deny[name]; hard {
			continue
		}
		allow[name] = struct{}{}
	}

	// 把未被豁免的默认拒绝项并入最终拒绝集。
	for name := range exemptable {
		if _, granted := allow[name]; granted {
			continue
		}
		deny[name] = struct{}{}
	}

	return ChildPolicy{
		Allow: sortedToolNames(allow),
		Deny:  sortedToolNames(deny),
	}
}

// sortedToolNames 把工具名集合展开为去重、稳定排序的切片。
//
// 先收集再排序，避免直接遍历 map 带来的随机顺序；sort.Strings 按 UTF-8 字节序比较，
// 对合法 UTF-8（含 CJK）字符串是确定且安全的。
func sortedToolNames(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
