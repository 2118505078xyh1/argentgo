package skills

// Prereq 描述一条技能声明的前置条件。
// Name 是条件的名字（例如工具名、环境变量名或配置项名），
// Kind 是条件的类别（例如 "tool"、"env"、"config"），
// Value 是条件期望的值或补充描述，具体含义由调用方的 PrereqEvaluator 解释。
// 它是一份纯数据，CheckPrereqs 与 PrereqSummary 都不会改动它。
type Prereq struct {
	Name  string
	Kind  string
	Value string
}

// PrereqEvaluator 判定一条前置条件是否已被满足，由调用方注入具体检测逻辑
// （查工具是否在 PATH、环境变量是否存在、配置是否设置等）。
// 本包不依赖任何具体环境，检测全部委托给这个函数。
type PrereqEvaluator func(Prereq) bool

// CheckPrereqs 按给定顺序逐条评估 prereqs，返回未满足条件切片的稳定顺序副本。
// eval 为 nil 时默认所有条件都已满足：不会调用任何回调，也不产生任何副作用，
// 返回 nil 的 unmet 与 nil 的 err。
// 当前 PrereqEvaluator 只返回 bool，没有失败渠道，因此 err 始终为 nil；
// 保留该返回值是为了让调用方无需在将来扩展检测语义时改动签名。
// 本函数是纯函数：不修改 prereqs，也不持有 eval 返回的任何状态。
func CheckPrereqs(prereqs []Prereq, eval PrereqEvaluator) (unmet []Prereq, err error) {
	if eval == nil {
		return nil, nil
	}
	for _, p := range prereqs {
		if !eval(p) {
			unmet = append(unmet, p)
		}
	}
	return unmet, nil
}

// PrereqSummary 按 Kind 统计 prereqs 中各类前置条件的数量。
// 空切片（或 nil）返回空的、非 nil 的 map；Kind 为空字符串的条件也会单独占一个键。
// 本函数是纯函数，不修改 prereqs。
func PrereqSummary(prereqs []Prereq) map[string]int {
	counts := make(map[string]int)
	for _, p := range prereqs {
		counts[p.Kind]++
	}
	return counts
}
