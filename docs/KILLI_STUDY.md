# killi 研究：可迁移思想与落地记录

本文件记录从 [`killi`](https://github.com/timwuhaotian/killi)（Rust 终端编码 agent）源码研究中提炼的工程思想，以及迁移到 argentgo 的结果。研究时间：2026-11-22 ～ 11-23。killi 源码快照位于 `D:\code\mpi\killi`，其设计决策见该仓库 `docs/decisions.md`（38 条）与 `docs/reference/` 两份 harness 清单。

## 核心结论

killi 的三条工程纪律：

1. **缓存前缀绝不破坏**：工具列表整会话固定、system prompt 冻结、history 只追加、提醒放末尾、所有"拦截"在工具执行期做。
2. **资源占用有界**：大文件拒绝、输出滚动上限、超时包含写入、杀整棵进程树、按 hash 去重。
3. **原始请求可复现**：日志记录实际发送的线协议字节，resume 回放字节而非重建语义。

argentgo 的底子并不弱：plan mode 已是"运行时拦截而非移除工具"、reminder 已在末尾、会话日志已单次写、thinking delta 已原地追加、参数强转已接线。差距主要在"把纪律变成 CI 断言"和"资源边界"。

## 已迁移（按提交顺序）

| 提交 | 来源 | 内容 |
|---|---|---|
| `17b0193` | 决策 35 | `read` 读前 `Stat` 检尺寸，>10 MB 拒绝并提示 `grep_ex`/`sed`，消除"文件+解码副本+行切片"数百 MB 峰值 |
| `7d6807f` | 决策 28/9 | prompt 预算护栏（system + 工具定义 ≤40 KB）+ 两条缓存前缀不变量（提示不随 cwd 变、`Specs()` 逐字节稳定） |
| `e77e844` | 决策 12/35 | `web_fetch` 读体前查 Content-Type，二进制零下载；模糊类型放行避免误伤 |
| `c3718b3`/`8ce2a0d` | 决策 35 | 钩子超时终止整棵子进程树；proc 导出 `ConfigureGroup`/`KillGroup`/`ProcessAlive`；修复 Windows `killTree` 顺序（先 `taskkill /T` 再终止作业对象），否则作业关联后启动的孙进程漏网 |
| `0f306ee` | 决策 26 | 把已有但只被测试调用的 `SetLoopDetection` 接成 `ARGENT_LOOP_DETECTION` opt-in，每次 Execute 按本次 env 显式设置 |
| `3254361` | 决策 15 | `eval history`：从 git 历史抽任务（commit message 当 prompt、新增测试种入父提交 worktree、`FileEquals` 防作弊、`go test` 评分）；`eval.Options` 加可注入 `WorkspaceFactory`（nil 默认路径逐字节不变） |
| `faefe3b`/`3e66c71` | 决策 21 | 请求字节快照：llm 三协议流式 marshal 点把实际发送的字节交给 context sink，opt-in 追加到会话；端到端测试验证 sink 字节与服务端实际收到的请求体逐字节相同 |
| `aff2cd2` | 决策 15 | `eval history --scripted`：离线脚本模式跑通 git→worktree→评分全流程，无需 API key |
| `00feb74` | 决策 15 | `eval history verify`：每个任务在父提交下应失败、参考提交下应通过，离线过滤无判别力任务 |
| `95d0e48` | 决策 21 | 快照钩子 benchmark（默认 3.4ns、开启 9.7ns，均零分配），确认不成为热路径瓶颈 |
| `33ef69f` | 决策 15 | 历史任务检查改为只跑新增测试所在的包而非 `go test ./...`，超时提到 120s，避免全新 worktree 首次编译超时误判 |
| `c359f9e` | 决策 15 | `eval history --verify`：跑模型前离线自检，跳过无判别力任务，避免浪费 token |
| `b47494a` | 决策 20 | `ARGENT_SUBAGENT_SHARED_PROMPT`：子代理以父级 system 为前缀、角色约束追加末尾，首请求复用缓存；system 构造抽成可测纯函数 |
| `c5677c1` | 决策 15 | `--verify` 复用已抽取的任务集，不再重复跑 git log/diff/show |
| `325f6ad` | 决策 21 | `sessions snapshots <id>` 只读子命令：列出请求快照的协议/模型/字节数/hash 摘要，不打印完整 body |
| `650dd88` | — | 本研究总结文档 |

另：`9d8945a` 修复项目 TODO #28 的审计脱敏缺口（`--password value` 形式的 CLI flag 值此前明文进入审计），虽非 killi 迁移，但同属本轮工程纪律加固。

## 未迁移及原因

| 项 | 来源 | 原因 |
|---|---|---|
| MCP 单网关工具 | 决策 11 | 需重写 `internal/app/mcp.go`（956 行）的注册、`RefreshTools` 与审批链；改动期间有代理正在改 MCP 审批逻辑，无人监督下风险过高 |
| resume 字节回放 | 决策 21 | 已记录快照，但回放需新增 `ChatRaw` 接口并改三协议客户端 + agent 首请求分支，热路径风险高；留待有监督阶段 |
| 子代理复用主工具表 | 决策 20 | prompt 共享已落地（`b47494a`）；工具表完全复用仍需改 `Options`/`taskTool`/`runTaskOnce` 多处，留待有监督阶段 |
| undo 用隐藏 git 快照 | 决策 24 | argentgo 已有 checkpoint 机制（文件旧字节存会话），功能等价但机制不同；切换代价大于收益 |
| bash 输出尾部遍历 | 决策 35 | `internal/proc.cappedBuffer` 已对每个流做 64 KB 头尾上限，同类内存峰值问题已解决 |

## argentgo 原本就做对的点

- plan mode 运行时拦截、reminder 末尾注入（`budget_reminder.go`）
- 会话 JSONL 单次写（`session_append.go`）、单次 `json.Marshal` + 一次 `Write`
- `strings.Builder` + `WriteString` 原地追加 thinking delta（无 O(n²)）
- `io.LimitReader` 限制 fetch 体积、参数强转 `tool_argument_coercion.go` 已接线
- skill 名字-only 索引 + 按需加载（`internal/skills`）

## 验证结果（2026-11-23 02:24）

- 全量快验证（`gofmt -l .` / `go vet ./...` / `go test -p 4 ./... -count=1`）：**全绿**，11364 测试通过。
- 针对性 race（`./internal/llm ./internal/agent ./internal/hooks ./internal/proc ./internal/session`）：**全绿**，5805 测试通过。
- Linux 交叉编译（`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...`）：**通过**。
- 全量 `go test -race ./...`：2 处竞争均在 `internal/server/event_persist_test.go`（测试 goroutine 并发读 `httptest.ResponseRecorder` 的 buffer，SSE handler 在 `event.go:456` 并发写），是测试侧竞争，非本轮改动引入；普通全量测试全绿。
- `internal/computer` 的 `TestCapture` 曾在 headless 环境下 BitBlt 失败，后续全量验证已通过（环境相关，非回归）。

## 复现方式

- `eval history <repo> --scripted`：离线跑一遍 git→worktree→评分流程（任务预期失败，因为脚本不修 bug）
- `eval history verify <repo>`：离线自检任务判别力（无需模型）
- `eval history <repo> --scripted --verify`：先自检过滤，再离线跑有效任务
- `ARGENT_LOOP_DETECTION=1`、`ARGENT_REQUEST_SNAPSHOT=1`：开启对应 opt-in
