package commands

// 为 app 层注册内置斜杠命令。
//
// Discover 仅从磁盘读取 markdown 文件；像 /context 和 /review
// 这样的内置描述符没有对应文件，调用方必须显式注册它们。
// NewSetWithBuiltins 将该步骤集中在一处：app 通过
// 该辅助函数构建命令集，并将匹配的斜杠输入路由到内置钩子。
// internal/cli 中 `argentgo commands` 的列举路径保持不变，仍然只
// 列举磁盘上的命令，因此用户可见的输出没有任何变化。

// NewSetWithBuiltins 从各来源发现命令，并实例化注册表中的所有内置描述符
// （当前为 /context 和 /review）。实例化顺序由注册时显式给出的 order 决定，
// 与旧的 set.Add(ContextCommand(), ReviewCommand()) 完全一致；显式声明的
// markdown 命令仍按名称优先，因为 Add 从不覆盖已有条目。
func NewSetWithBuiltins(sources []Source) *Set {
	set := Discover(sources)
	factories := builtinRegistry.Factories()
	cmds := make([]Command, 0, len(factories))
	for _, factory := range factories {
		cmds = append(cmds, factory())
	}
	set.Add(cmds...)
	return set
}
