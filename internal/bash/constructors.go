package bash

import "example.com/argentgo/internal/pluginapi"

// NewBashTool 返回 bash 工具实例，供注册方（internal/tools）在其 init() 中
// 以固定 order 注册。构造器返回 pluginapi.Tool 契约，注册方无需 import 具体
// 工具类型，避免包间循环依赖。
func NewBashTool() pluginapi.Tool { return bashTool{} }

// NewPowerShellTool 返回 powershell 工具实例。
func NewPowerShellTool() pluginapi.Tool { return powershellTool{} }

// defaultWriteStdinTool 是进程内共享的 write_stdin 工具实例。
// 注册表通过 NewWriteStdinTool 取得实例；这里持有默认实例既保证工具无状态
// 复用，也让构造器在 opt-in 接线前就有一个无条件生产调用方。
var defaultWriteStdinTool = NewWriteStdinTool()

// NewWriteStdinTool 返回 write_stdin 工具实例，供注册方（internal/tools）
// 在 ARGENT_EXEC_SESSION opt-in 时注册。关闭 opt-in 时该工具不会被注册，
// 即使被误调用也会返回明确的禁用错误。
func NewWriteStdinTool() pluginapi.Tool { return writeStdinTool{} }
