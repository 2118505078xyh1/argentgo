// Package app 把迁移过来的各个包接入 CLI：配置解析、提供方选择、工具注册表、
// 安全链、会话存储以及 agent 循环。
//
// 这是唯一知道所有其他包的包；除构造之外，不应让它承担其他行为。
//
// 实现按职责拆分到各个 app_*.go 文件中：
//   - app_types.go：共享的已解析选项和提供方类型
//   - app_resolve.go：配置/环境解析与数值辅助函数
//   - app_chatter.go：LLM 客户端构建与回退包装
//   - app_prompt.go：系统提示与输出 schema 加载
//   - app_runner.go：运行器工厂与 cli.Runner 实现
//   - app_execute.go：每次运行的 Execute 流程与审批器
package app
