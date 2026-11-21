package pluginapi

import (
	"errors"
	"fmt"
)

// ErrOrderDrift 表示实际加载顺序与依赖声明推导出的期望顺序不一致。
// 调用方可用 errors.Is 匹配，并通过错误文本获得首个错位位置或长度差异。
var ErrOrderDrift = errors.New("pluginapi: order drift")

// ValidateFeatureDeps 只校验 Feature 声明的合法性，不返回排序结果。
//
// 校验项与 TopoSort 完全一致，并复用其哨兵错误（可用 errors.Is 匹配）：
//   - ErrDuplicateFeature：重名功能；
//   - ErrMissingDependency：依赖了未声明的功能；
//   - ErrCycle：依赖图中存在环。
//
// 声明合法时返回 nil。本函数是纯函数，不修改 features 及其 Deps。
func ValidateFeatureDeps(features []Feature) error {
	_, err := TopoSort(features)
	return err
}

// ExpectedOrder 校验 current 是否与依赖声明推导出的期望加载顺序逐项一致。
//
// 先调用 TopoSort 得到期望顺序；若声明本身非法，则原样返回 TopoSort 的哨兵错误
// （不会伪装成 ErrOrderDrift）。声明合法但顺序不符时返回 ErrOrderDrift：
//   - 长度不同：错误文本含实际长度与期望长度；
//   - 长度相同：错误文本含首个错位索引 i 以及该处的 got/want 名称。
//
// nil 与空切片视为长度相同的空顺序。本函数是纯函数，
// 不修改 features 及其 Deps，也不修改 current。
func ExpectedOrder(features []Feature, current []string) error {
	want, err := TopoSort(features)
	if err != nil {
		return err
	}
	if len(current) != len(want) {
		return fmt.Errorf("%w: length mismatch: got %d, want %d", ErrOrderDrift, len(current), len(want))
	}
	for i := range want {
		if current[i] != want[i] {
			return fmt.Errorf("%w: first mismatch at index %d: got %q, want %q", ErrOrderDrift, i, current[i], want[i])
		}
	}
	return nil
}

// StableKey 返回 Feature 的 Name+Order 稳定键，供日志与诊断使用。
// 格式为 "name@order"，仅由入参决定，与切片排列顺序无关；
// Name 含中文等 Unicode 字符时按原样保留，不做转义或截断。
func StableKey(f Feature) string {
	return fmt.Sprintf("%s@%d", f.Name, f.Order)
}
