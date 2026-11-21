# omp (oh-my-pi) Autonomous Memory 文档 — 抓取于 2026-11-21

> 只读研究资料，来源 raw.githubusercontent.com/can1357/oh-my-pi/main/docs/memory.md

## 五种 memory backend（默认 off）
- off / local（项目级摘要+教训）/ hindsight（远程 bank）/ mnemopi（本地 SQLite）/ sharpshooter（摩擦门控项目决策文件）

## local backend 两阶段模型驱动管道（重点）
**Phase 1 — per-session extraction**：每个自上次处理后变更的历史 session，模型读 session history 提取 durable signal：技术决策、约束、已解决失败、重复工作流。跳过太近/太老/活跃/超限的 session。产出 raw memory block + synopsis。

**Phase 2 — consolidation**：第二个模型 pass 读所有 per-session 提取，产出三个磁盘产物：
- `MEMORY.md` — 策展的长期记忆文档
- `memory_summary.md` — session 启动时注入的紧凑文本
- `skills/` — 可复用程序化 playbook，每个独立子目录
- `learned.md` 单独维护不被 consolidation 覆盖

用 lease + heartbeat 防多进程重复运行；陈旧 skill 目录自动清理；写盘前对常见 secret/token 模式脱敏。

## 注入
session 启动时若有 consolidated summary 或 lesson，作为 **Memory Guidance** block 注入 system prompt（共享 summaryInjectionTokenLimit=5000）。启动 consolidation 若晚于首个 prompt build，活跃 session 的 summary snapshot 刷新，但已捕获 lesson 钉在初始 snapshot（并发 learn 只影响后续 session）。
- 指导原则：记忆是 heuristic context，不是当前 repo 状态的权威；记忆改变计划要引用 artifact path + 配当前 repo 证据；冲突时 repo state/user instruction 优先。

## learn 工具（autolearn.enabled）
保存显式 durable lessons 到项目 learned.md，newest-first、去重、secret-redacted、上限 100 条、下一 session 生效，**不改变活跃 session 的 prompt-cache prefix**。每条内容上限 2000 字符 + context 400 字符。

## memory:// URL scheme（agent 用 read 工具读）
memory://root / memory://root/MEMORY.md / memory://root/learned.md / memory://root/skills/<name>/SKILL.md / memory://<memory-id>

## /memory slash 命令
view/stats/diagnose/queue/sync/clear/reset/enqueue/rebuild/mm

## 配置
maxRolloutAgeDays=30 / minRolloutIdleHours=12 / maxRolloutsPerStartup=64 / threadScanLimit=300 / maxRawMemoriesForGlobal=200 / stage1Concurrency=8 / phase1InputTokenLimit=4000 / fallbackTokenLimit=16000 / summaryInjectionTokenLimit=5000

## 模型选择（piggyback role system）
Phase1 extraction=default role；Phase2 consolidation=smol role（回退 default/current/first）。

## sharpshooter backend（值得单独学）
三个短决策文件 architecture.md/product.md/style.md 注入 system prompt。每个主 session user prompt 送 extraction model 记录 durable decisions（架构/产品/风格/约束/拒绝方案/纠正）为 delta 队列；scheduler 每分钟检查，intervalMinutes=5 后一次 model pass 应用到三文件。minPromptLength=16（CJK 按显示字符算），injectionTokenLimit=15000。

## 与 argent-go memory 对比要点
argent-go 有 memory.Bank/Recall/Spill + extractive synthesis（R 早期），但缺：①模型驱动的跨会话两阶段提取/综合；②自动生成 skills playbook；③memory:// URL 统一文件接口；④friction-gated 决策文件（sharpshooter）；⑤注入的 token 上限与 cache-prefix 保护纪律。
