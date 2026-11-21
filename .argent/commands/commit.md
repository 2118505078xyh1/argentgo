---
description: 审查当前 git 改动，按 conventional commit 生成提交信息，经用户确认后提交
argument-hint: "[可选的提交范围说明，例如 'fix parser timeout']"
allowed-tools: bash, read, grep, glob
---

按以下步骤完成一次提交。**用户确认之前，绝不执行 git commit。**

## 1. 收集改动

- 运行 `git status --short` 查看工作区状态。
- 运行 `git diff --stat` 与 `git diff` 查看未暂存的改动；若有已暂存内容，用 `git diff --cached` 一并查看。
- 改动很多时按文件分组阅读，关注改动的**意图**，不要逐行复述补丁。
- 检查是否有不应提交的文件（构建产物、`node_modules`、临时文件、含密钥的配置）。

## 2. 生成提交信息

按 conventional commit 格式：

    <type>(<scope>): <imperative summary>

- `type` 取 `feat` / `fix` / `docs` / `refactor` / `test` / `chore` / `perf` / `build` / `ci`。
- `scope` 可选，用改动所属的包或模块名（如 `agent`、`llm`、`tools`）。
- 摘要用现在时祈使句（`reject path escapes`，不是 `rejected` 或 `rejects`），不超过 72 字符，不以句号结尾。
- 正文（可选）解释**为什么**改，而不是重复 diff 说了什么；多个要点用列表写清。
- 若有破坏性变更，脚注用 `BREAKING CHANGE: ...`。

用户参数：$ARGUMENTS

若 $ARGUMENTS 非空，把它作为提交范围/意图的提示；若它与实际 diff 矛盾，以 diff 为准并向用户说明。

## 3. 确认

把拟定的提交信息（完整标题与正文）展示给用户，明确询问"是否提交"。**在用户明确同意之前停止，不做任何提交操作。**

## 4. 提交

用户确认后：

1. `git add` 仅本次相关的文件（不要用 `git add -A` 顺带提交无关改动）。
2. `git commit` 使用上面确认的信息。
3. 提交后运行 `git status --short` 确认工作区状态符合预期。

## 禁止

- 不提交构建出来的二进制、`node_modules` 或任何含敏感信息的文件。
- 不修改提交信息中未经用户确认的内容。
- 不执行 `git push`（除非用户在参数或后续确认中明确要求）。
- 不把格式改动或无关重构混进本次提交。
