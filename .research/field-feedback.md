# 真实使用反馈（用户在 argent 0.3.2 上的实测对话，2026-11-21）

> 只读参考。注意：反馈基于旧版 0.3.2，部分问题在当前主树（1bfa2e0）可能已修复，需逐条核实。

## 对话中的实测现象
- web_search 默认后端 DuckDuckGo 超时（国内网络），`web_search` 报 "failed from every source"。
- 用户改用 `curl.exe` 直连：baidu.com/bing.com/api.github.com 均 200，可抓网页。
- `Get-Content internal/skills/skills.go` 得到乱码 "鍙戠幇纾佺洏涓婄殑 SKILL.md"（UTF-8 被 GBK 误解）。
- 两次工具输出静默缺失：`Get-ChildItem | Where-Object` 返回 "(no output)"；多语句命令只输出第一行。
- 用户环境：`~/.agents/skills` 存在（含 longform-memory 跨项目记忆方法论、hindsight-coding-agent），但 `~/.argentgo/skills` 不存在。项目内 `.argent/skills`（code-review/debug-workflow/test-writer）正常。

## agent 给出的四条判断（供核实，非结论）
1. **P0 中文入站解码**：出站 grapheme 已修（R20），入站未修。修法：Windows 执行前后固定 OutputEncoding/$OutputEncoding/chcp 65001；read 侧加 BOM 探测 + encoding 参数。
2. **工具输出完整性对账**：静默缺输出是最昂贵失败模式（模型基于缺失证据判断且永不报错）。在 clipBashOutput/token 估算处加 stdout 字节数/行数对账，不一致重试一次并标 output_truncated=true。
3. **默认零搜索**：诚实的规则（无 key 不联网）导致模型每次要手写 curl 绕过。建议加零 key best-effort 后端（Bing/Sogou/公共 SearXNG），标"尽力而为可能失败"，与确定性工具分开治理。
4. **技能发现路径没对齐生态**：DefaultHomeSubdir=.argentgo/skills 但现实用 .agents/skills。跨项目记忆方法论读不到。建议多路径只读叠加发现（.argentgo/skills + .agents/skills + .claude/skills），索引保持一行一个，不违反提示词克制。

## agent 独立判断
差距不在"能不能做"，而在"每次新会话重新付一遍环境税"：编码、发现、连通性、输出完整性。这四项不增行数、不加提示词、不动 22 检查点，却直接决定单对话模型上限。若只做一件，选第 1 条（污染模型看到的世界本身）。

## 当前主树核实状态（主会话已查）
- 行数口径：主树 356,835（两种统计法一致），旧版 0.3.2 的 327,092 是旧快照，口径无误。
- 中文入站：tools_builtin_powershell.go:39 已有 powerShellUtf8Prefix（[Console]::OutputEncoding=UTF8; $OutputEncoding=UTF8）——但 bash 走 cmd /c 的路径是否设编码待核实。
- 技能路径：skills.go:27 DefaultHomeSubdir=.argentgo/skills，确实无 .agents/skills/.claude/skills——当前版本仍缺（真实差距）。
- 输出对账：全仓无 stdout 字节/行数对账——当前版本仍缺（真实差距）。
- 零 key 搜索：有 Brave/Tavily/SearXNG presets 但都需 key；DDG 无 key 后端国内超时——部分成立。
