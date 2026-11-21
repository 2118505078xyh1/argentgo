package commands

import "github.com/2118505078xyh1/argentgo/internal/pluginapi"

// builtinRegistry 保存内置斜杠命令的构造器。各内置命令在自己的 init() 中
// 静态注册；NewSetWithBuiltins 每次调用都重新实例化一份，避免共享可变状态。
var builtinRegistry pluginapi.Registry[Command]

// 内置命令的显式顺序：数值越小越先实例化。order 固定写死，因此
// 命令在 golden 快照中的顺序不受跨文件 init() 执行顺序的影响。
// 这两个值对应旧版 set.Add(ContextCommand(), ReviewCommand()) 的实参顺序。
const (
	builtinOrderContext = 10
	builtinOrderReview  = 20
)
