# `argentgo init` 向导设计（P2-3，待实施）

> 产品决策：把当前"三行静态提示"升级为"可延后的交互式初始化"，让新用户第一次就能走通到成功调用。依据：首次体验审计痛点 #1/#4/#5，竞品 crush/cline 的 onboarding 范式（可延后、分步、说明用途）。
>
> 现状源码：`internal/cli/cli.go:59-106` 的 `maybeFirstRunGuide` 在无参数+TTY+无配置+无 `.onboarded` 标记时向 stderr 打印三行（login/tui/doctor），写完标记即消失；TUI/GUI 内无引导。

## 产品原则

1. **可延后**：向导不拦路。任何步骤可跳过，稍后 `argentgo init` 随时可再运行。
2. **说明用途**：每一步告诉用户"这会生成什么、为什么需要"，而非空问选哪个。
3. **零配置优先**：优先推荐本地免 key provider（Ollama/LM Studio）与设备码登录，再给 API key 路径。
4. **默认最好**：探测到的本地服务/已登录态自动预选，用户只需回车确认。
5. **不引入依赖**：纯标准库；不复制 internal 逻辑到 cli（经现有 API 调用）。

## 交互流程（分步）

```
$ argentgo init

 Argentgo 初始化向导（随时可 Ctrl+C 退出，之后运行 argentgo init 可继续）

 [1/4] 选择模型提供方
   1) Ollama（本地，免 API key）        ← 探测到 127.0.0.1:11434 时预选
   2) LM Studio（本地，免 API key）     ← 探测到 127.0.0.1:1234 时预选
   3) OpenAI / Anthropic / ...（需登录或 API key）
   4) 跳过，稍后再配置
   选择 [1]:

 [2/4] 凭据
   （本地 provider）无需凭据，已为你配置 base URL。
   （云端 provider）1) 设备码登录 2) 输入 API key 3) 稍后再说

 [3/4] 生成配置
   将写入 ~/.argentgo/config.json（带注释，只含必要项）。
   现有配置不会被覆盖，只补充缺失项。
   确认 [Y/n]:

 [4/4] 自检
   正在运行 argentgo doctor ...
   ✓ 配置  ✓ 存储  ⚠ 凭据：未配置（可运行 argentgo login --device <provider>）
   完成。现在可以：argentgo tui  或  argentgo "帮我写个函数"
```

## 实施要点

| 步骤 | 复用 | 落点建议 |
|---|---|---|
| provider 探测 | `internal/provider` 模板（`APIKeyEnv` 为空的本地模板）；本地端口探测用 `net.DialTimeout`（stdlib） | `internal/cli/init.go` |
| 凭据 | `internal/auth` 的 `login` / device code 流程（`device_login.go`） | `internal/cli/init.go` |
| 配置生成 | `internal/config` 的 load/save + 脱敏；分层写入最高层文件 | `internal/cli/init.go` |
| 自检 | `internal/cli/doctor.go` 的 10 项检查（本轮刚加 credential 项） | 复用 `runDoctor` |
| 首启入口改造 | `maybeFirstRunGuide` 三行末尾加"运行 `argentgo init` 可交互式初始化"；不自动进入向导（避免拦路） | `internal/cli/cli.go` |

## 非目标（明确不做）

- 不做图形化向导（CLI 交互即可）。
- 不自动覆盖用户已有配置。
- 不强制登录（可延后）。
- 不在向导里塞高级选项（工具/MCP/hooks 配置留给文档）。

## 验收

- `argentgo init` 在无配置环境走完四步后用脚本化 provider 验证生成的配置能跑通一次调用。
- 选择"跳过"后不写配置、不写 `.onboarded` 标记、退出码 0。
- 已有配置时只补充缺失项，不覆盖现有值（golden 测试）。
- 非 TTY 环境（管道/重定向）下 `init` 明确报错"需要交互终端"，不挂起。
- i18n 硬编码基线不增长（文案走 i18n 表）。
