package tools

// ExecutionMode 描述工具在并发调度中的执行方式。
// 零值 ""（ExecutionModeUnspecified）表示工具未声明执行方式，
// 调用方应据此退回到自己的默认调度策略。
type ExecutionMode string

const (
	// ExecutionModeUnspecified 表示工具未声明执行方式。
	ExecutionModeUnspecified ExecutionMode = ""
	// ExecutionModeShared 表示工具是本地纯读操作，可与其他 shared 工具并发执行。
	ExecutionModeShared ExecutionMode = "shared"
	// ExecutionModeExclusive 表示工具需要独占执行。
	ExecutionModeExclusive ExecutionMode = "exclusive"
)

// modes 保存内置工具的静态执行方式声明。
//
// 这里只声明少数本地纯读工具为 shared；usage/history/lsp_*/memory_*
// 等具备状态或依赖外部数据源的工具绝不声明为 shared。该表是编译期
// 常量数据，不随任何注册表实例变化，也不影响 Specs()/Names()/Run()
// 的现有行为。
var modes = map[string]ExecutionMode{
	"read":       ExecutionModeShared,
	"ls":         ExecutionModeShared,
	"glob":       ExecutionModeShared,
	"grep":       ExecutionModeShared,
	"find":       ExecutionModeShared,
	"git_status": ExecutionModeShared,
	"repo_map":   ExecutionModeShared,
	"diff":       ExecutionModeShared,
}

// DeclaredMode 返回 name 的静态声明执行方式。
// 未声明的工具返回零值 ExecutionModeUnspecified。
func DeclaredMode(name string) ExecutionMode {
	return modes[name]
}

// AllDeclaredModes 返回静态声明的副本，调用方修改结果不会影响声明表。
func AllDeclaredModes() map[string]ExecutionMode {
	out := make(map[string]ExecutionMode, len(modes))
	for name, mode := range modes {
		out[name] = mode
	}
	return out
}

// SetExecutionMode 为当前注册表设置 name 的执行方式覆盖。
// 覆盖仅作用于本实例，不影响其它 New 出来的注册表，也不改变静态声明表。
func (r *Registry) SetExecutionMode(name string, mode ExecutionMode) {
	if r.modes == nil {
		r.modes = make(map[string]ExecutionMode)
	}
	r.modes[name] = mode
}

// ExecutionMode 返回当前注册表中 name 的执行方式覆盖。
// 未设置覆盖时返回零值 ExecutionModeUnspecified（默认全部未声明）。
// 静态声明请通过 DeclaredMode 查询：两层刻意分离，使运行时覆盖不会
// 污染编译期声明，调用方可按自身策略决定是否回退到声明值。
func (r *Registry) ExecutionMode(name string) ExecutionMode {
	return r.modes[name]
}
