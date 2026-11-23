# argentgo 优化 TODO（竞品驱动 · 第三轮 · 仅未做项）

> ✅ **完成状态（2026-11-23）**：本文件第三轮 12 条已全部落地并纳入 **v0.8.2**（行数锚点 500,037）。清单保留作实现记录，下列"现状/建议/验收"均为当时快照，不再表示未做项。后续待办见 `docs/TODO_REVIEW_2026_11_23.md`（第四轮审查）与 `docs/PRODUCT_ROADMAP.md`。

> **生成方式**：2026-11-23，6 个只读代理深挖 codex / opencode / pi-mono / oh-my-pi / cline / crush / kilocode / OpenHands 的 provider 协议、工具生态、会话协作、性能、安全、开发者体验六个维度（均带 `路径:行号` 证据），逐条核对 argentgo 源码去重后按「真实缺口 × 用户可感知 × 代码量 × 风险可控」筛选。本轮目标：实质功能补齐 + 代码行数冲击 50w（发版锚点，现 490,401）。
>
> **硬约束**：核心 stdlib-only、零新增 Go 模块、无 cgo、单二进制、自研 shell 不外包；native/ 是唯一 MyGo 例外。
> **默认化原则**：改善体验的新功能默认开启、参数取最优值、用户零选择；环境变量仅 `=0` 紧急回退。
> **优先级**：P0 = 实锤 bug / 安全绕过 / 数据丢失；P1 = 明显缺口；P2 = 改进。工作量 S/M/L。

**统计**：第一波 12 条（本文件）；后续波次（多凭据池、事件溯源、Code Mode、SigV4 等）在完成后追加。

---

## 一、性能与资源（P0：真实 OOM / 大仓退化）

### 1. grep/glob 流式扫描 + 大文件上限 + 二进制前置检测 `[P0/L]`（学自 oh-my-pi xfs）
- **竞品证据**：`oh-my-pi/crates/xfs/src/grep/mod.rs:53` `MAX_FILE_BYTES=4MB`；`scan.rs:24-31` `BINARY_WINDOW=64KB` `STREAM_LINE_LIMIT=64MB`；`run.rs:172-214` 小文件单次读、超限延后二次流式。
- **argentgo 现状**：`internal/tools/tools_builtin_search.go:82-91` `os.ReadFile` 整文件 + `strings.ContainsRune(string(data),0)` + `strings.Split` 三次全量拷贝，**无大小上限**——单个 1GB 日志可分配数 GB。`grep_ex.go:124` 同模式。
- **问题**：OOM 风险；无 `offset/cap` 无法早停省内存；无上下文行/列截断。
- **建议**：纯 Go `bufio.Reader` 分块扫描（跨块行缓冲，行 >64MB 跳过）；<4MB 单次读（不 ReadFile 双拷）；>4MB 延后流式；前置 64KB NUL 检测；实现 `before/after` 上下文与列截断。不用 mmap（无 stdlib、跨平台风险）。
- **验收**：100MB 文件 grep 峰值堆 <16MB（`runtime.MemStats`）；二进制不输出乱码；现有 200 行上限测试兼容。

### 2. regex 容错编译（消毒 + 字面量回退） `[P1/M]`（学自 oh-my-pi xfs）
- **竞品证据**：`oh-my-pi/crates/xfs/src/grep/engine.rs:112-152` 降级链：regex → 括号转义重试 → `regex::escape` 字面量兜底；`:213-293` 消毒 `${x}`、孤立括号。
- **argentgo 现状**：`tools_builtin_search.go:71-74` `regexp.Compile` 失败直接报 `bad pattern`，无消毒无回退。模型常传 `${HOME}`/`foo(`/`(?=...)`（RE2 不支持 lookaround）导致整轮失败。
- **建议**：stdlib 降级链：消毒 braces（保留 `{n,m}`/`\p{}`）→ 编译 → 孤立括号转义重试 → `regexp.QuoteMeta` 字面量兜底；lookaround 给可读提示。
- **验收**：表驱动 `${x}`/`fetchProvider(`/`(?=x)`/合法 `a{2,4}`/Unicode `\p{Greek}`。

### 3. 并行目录遍历 + 进程级目录列表缓存 `[P1/L]`（学自 oh-my-pi xfs）
- **竞品证据**：`oh-my-pi/crates/xfs/src/walk/parallel.rs:1-20` N worker 共享队列、就地 settle；`cache.rs:1-18` 内核 stamp 失效、`MAX_BYTES=64MB`、`RACY_WINDOW_NS=50ms`、`SHARDS=64`。
- **argentgo 现状**：`walkFiles` 串行 `filepath.WalkDir`；无目录列表缓存（`file_freshness.go` 跟踪单文件且无生产调用）。
- **建议**：`internal/tools` 新增固定 GOMAXPROCS worker + `chan dirJob` + pending 计数收尾 + `sync.Pool` scratch + context 取消；新增 `dircache.go`：64 分片 map、目录 ModTime/inode stamp 校验、50ms 竞态窗口不缓存、64MB 字节预算驱逐、ignore 源 stamp 校验。
- **验收**：10 万文件仓 grep wall time 降 ≥2×；同目录二次 glob readdir 次数为 0（fake FS 断言）；外部建文件后下次命中；8 goroutine 无 race。

### 4. 搜索截断信号 + 超时建议 `[P2/S]`（学自 oh-my-pi）
- **现状**：`grep_ex.go:90,176`/`glob.go:49` 到上限即停，无 `truncated` 标志；超时无行动建议。
- **建议**：统一返回 truncated + 上限；`limit+1` 探测；超时中文建议。保持现有输出格式兼容。

---

## 二、安全边界（P0：网络维度断链）

### 5. 声明式网络出口策略引擎 `[P0/L]`（学自 codex / killocode）
- **竞品证据**：codex `config/src/permissions_toml.rs:337-359` `NetworkToml`（domains allow/deny、method）；`core/src/network_policy_decision.rs:46-71` 结构化拒绝原因。
- **argentgo 现状**：`internal/security` 无网络策略原语；`web_fetch.go:144-157` 仅硬编码私网拒绝；三沙箱是文件/命令级，网络维度审计链断裂。
- **问题**：提示注入可诱导 `web_fetch` 访问攻击域名；`allowLocal:true` 探测内网/云元数据；web_search/MCP HTTP-SSE 出口各自为政。
- **建议**：`internal/security/netpolicy.go`：`{decision, hostGlob, port?, protocol, method?}` 复用 GlobMatch，strictest-wins；`CheckURL` 纯函数；web_fetch/web_search/MCP HTTP-SSE 统一接 Chain（决策→审批→审计）。
- **验收**：deny 域即使 allowLocal 也拒；私网默认拒；allow 免审批；GOOS=linux 交叉构建。

### 6. 网络代理沙箱 + 域名级即时审批 `[P1/L]`（学自 codex network-proxy）
- **竞品证据**：codex `network-proxy/src/http_proxy.rs:227-300` CONNECT 逐次走 host policy + BlockedRequest 审计；`credential_broker.rs:1282-1298` 按目标裁剪凭据。
- **argentgo 现状**：`call_profile.go:47-50` 仅工具名黑名单；`sandbox_violation.go` 从 stderr 猜网络。bash/curl/node fetch/python urllib 可任意绕过工具级封禁。
- **建议**：`internal/netproxy`（stdlib net/http + CONNECT 隧道）：随机端口；`HTTP_PROXY/HTTPS_PROXY/NO_PROXY` 注入**自研 shell 子进程 env（shell 本体不动）**；每次出网走 Chain 域名审批；Limited 模式限 GET/HEAD/OPTIONS；审计 + 取消钩子。
- **验收**：`curl https://blocked.example` 得 403 并触发审批；NO_PROXY 直连；取消无泄漏；GOOS=linux 构建。

### 7. 破窗规则：RequireHuman 不可被 yolo/auto 自动批准 `[P1/M]`（学自 codex）
- **竞品证据**：codex `execpolicy-rules/src/decision.rs:9-19` `PromptUser` = "fresh human decision, independent of configured reviewer"。
- **argentgo 现状**：`policy.go:512-524` 注释明确"硬 deny 降级 Ask 在 --yolo 下被自动执行"；无"不可自动化"判决类型。
- **建议**：`Decision` 旁加正交 `RequireHuman` 标志；Chain 遇此标志绕过自动批准与缓存 grant，强制真人审批；config_guard/sensitive 操作默认打标；无交互通道则拒绝。
- **验收**：yolo 下带标志仍弹审批；无交互通道被拒；不带标志行为不变。

### 8. 能力分键授权（read grant 不能复用为 write） `[P1/M]`（学自 oh-my-pi）
- **竞品证据**：oh-my-pi `approval-cache.ts:24-27` `approvalGrantKey(capability, subject)` `\0` 分隔；`:145-162` symlink 解析到最深已存在祖先。
- **argentgo 现状**：`persistent_approver.go` scope 是工具名/命令前缀，无 read/write/exec 能力维度，不解析 symlink。
- **建议**：`ScopeForPath(tool, path)` → `capability\0canonicalPath`；`filepath.EvalSymlinks` 逐级向上（缺失走最深已存在祖先），目录键带尾部分隔符，Windows 小写。
- **验收**：同路径 read always 后 edit 仍 Ask；symlink 与真实路径共享授予；未创建子路径沿用祖先。

---

## 三、可观测与 token 准确性（P1）

### 9. Always-on 采样 profiler（环形缓冲 + 火焰图） `[P1/M]`（学自 oh-my-pi / crush）
- **竞品证据**：oh-my-pi `natives/src/prof.rs:1-21` 常驻环形缓冲 `MAX_SAMPLES=10000`；`:94-130` folded stack 聚合。
- **argentgo 现状**：无运行时 profiler（25 bench 是开发期基准，非运行时采样）。
- **建议**：`internal/prof`（stdlib）：固定容量环形缓冲 `[10000]Sample{region,dur,ts}`，`Region(name)` 返回 guard；`Snapshot(window)` 聚合分 region 耗时/调用数/占比，输出 markdown；标注 agent-loop/llm-stream/tool-run/compact/search/grep 热区；默认开启，热路径开销 <2%（bench 断言）。
- **验收**：跑一轮 eval 后 Snapshot 各 region 总和 ≈ wall time；缓冲写满内存恒定；并发无 race。

### 10. token 批量并行计数 + 单缓冲分摊 `[P1/M]`（学自 oh-my-pi / codex）
- **竞品证据**：oh-my-pi `natives/src/tokens.rs:77-126` 单缓冲读入 + `PARALLEL_BATCH_MIN=16` 并行阈值；codex `history.rs:881-916` 按 item 聚合 + saturating_add。
- **argentgo 现状**：`tokenize.CJK` 启发式，无批量 API/并行/单缓冲；agent 预算对一批消息逐个估算。
- **建议**：`internal/tokenest`：`CountBatch([]string) []int` 单 `[]byte` + span 切片；≥16 且 CPU>1 才 fan-out；改进启发式 ASCII≈len/4、CJK≈1/字；`EstimateItems` 聚合 + saturating_add。不嵌入第三方词表。
- **验收**：批量 1000 条 < 串行×0.6；16 条以下串行；CJK/ASCII 混合与现有预算不冲突。

---

## 四、开发者体验（P1/P2）

### 11. BM25 工具发现 + schema 文本索引 + 延迟暴露 `[P1/M]`（学自 pi-mono / codex）
- **竞品证据**：pi-mono `tool-search/tool.ts:22-45,78-120` 索引名称/描述/schema 属性名，BM25 排序后加载到下轮工具集；codex `tool_search.rs:14-17` 只索引 deferred。
- **argentgo 现状**：`tool_search.go:60-120` 手写启发式逐 token 比对工具名+描述首行，无 IDF/词干/字段加权，不看 schema；`model_tool_routing_wire.go` 一条静态规则无 exposure 元数据。
- **建议**：`internal/tools/toolrank`：ASCII camelCase 切词 + CJK bigram、停用词/复数归一、BM25（name>description>schema 字段加权）；`json.RawMessage` schema 跟随本地 `$ref` 收集属性名；`Tool` 加可选 `Exposure() direct|deferred|hidden`；命中 deferred 后并入本轮 SpecsFor。schema 索引 `sync.Map` 缓存。
- **验收**：稀有词优先、schema 属性命中、`web fetch` 命中 `web_fetch`、中文查询命中；34 默认工具 golden spec 不变。

### 12. 动态技能排序 + `$tool` 提及路由 `[P2/M]`（学自 codex）
- **竞品证据**：codex `dynamic_skill_selector/fielded_bm25.rs` 字段权重 `[8,4,1]`、`k1=1.2`、`b=0.75`；`skills/src/mentions.rs` `$tool-name` 提及。
- **argentgo 现状**：`skills/trigger.go` 路径 glob + 关键词子串，无相关性排序；无 `$name` 提及解析。
- **建议**：`internal/skills/selector.go`：fielded BM25 + 字符 n-gram 兜底，top-k 分数；`ParseMentions` 支持 `$name` 与 `[label](argent://skill/name)`；显式触发优先、BM25 补充。
- **验收**：固定语料 top-k 稳定；代码块 `$foo` 不误触发；benchmark smoke 不退化。

---

## 后续波次（本文件完成后追加）

多凭据池 + 主动刷新单飞（provider #3/#5/#6）、事件溯源投影 + 持久 inbox（会话 #2/#3）、Code Mode 受限工具 DSL（开发者 #1）、AWS Bedrock SigV4 + Copilot 令牌交换 + Vertex AI（provider #1/#2/#4）、托管 requirements 层（安全 #2）、Agentic fetch（工具 #5）、AST 解析缓存（性能 #6）、/models 动态目录 + ETag（provider #7）。

## 不做（明确取舍）

- mmap / PCRE2 / V8 Code Mode / 第三方 tokenizer 词表 / NFD Unicode（Go stdlib 无等价物或无 cgo 不可得）。
- marketplace / Node 插件 / 多设备同步（定位/依赖取舍）。
- 重复建设：provider 线协议解析、补丁修复阶梯、输出溢出缓存、byte replay 已具备。

## 验证口径

每条配表驱动测试 + 可执行验收命令；性能项配 bench 断言；每轮 `gofmt -l .`、`go vet ./...`、`go test -p 4 ./... -count=1`、archguard；门禁通过 ≠ 接线完成，逐项 grep 生产调用点。
