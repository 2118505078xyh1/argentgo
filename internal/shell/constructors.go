package shell

import "github.com/2118505078xyh1/argentgo/internal/pluginapi"

// NewBashTool 返回 bash 工具实例，供注册方（internal/tools）在其 init() 中
// 以固定 order 注册。构造器返回 pluginapi.Tool 契约，注册方无需 import 具体
// 工具类型，避免包间循环依赖。
func NewBashTool() pluginapi.Tool { return bashTool{} }

// NewPowerShellTool 返回 powershell 工具实例。
func NewPowerShellTool() pluginapi.Tool { return powershellTool{} }
