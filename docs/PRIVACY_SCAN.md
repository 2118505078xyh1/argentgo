# 隐私扫描门禁

`scripts/privacy-scan.ps1` 在提交前扫描 Git 追踪的文件，阻止私钥、token、本机路径与邮箱混入版本库。

## 用法

```powershell
pwsh -NoProfile -File scripts/privacy-scan.ps1
pwsh -NoProfile -File scripts/privacy-scan.ps1 -Filter '*.go'
```

- 干净：打印 `privacy scan clean: N tracked files`，退出码 0。
- 命中：打印 `文件:行号:类型`（`aws-access-key` / `github-token` / `openai-key` /
  `private-key` / `local-windows-path` / `local-unix-path` / `local-mailbox`），退出码非 0。

CI 已在 `.github/workflows/ci.yml` 的 `go` 任务里接入这一步。

## 检测项

| 类型 | 模式 |
|---|---|
| `aws-access-key` | `AKIA` + 16 位大写字母数字 |
| `github-token` | `gh[pousr]_` + 36 位字母数字 |
| `openai-key` | `sk-` + 20 位字母数字 |
| `private-key` | `-----BEGIN … PRIVATE KEY-----` + 100 字节以上密钥体 |
| `local-windows-path` | `C:\Users\<name>` |
| `local-unix-path` | `/home/<user>/` |
| `local-mailbox` | `*@local` |

## 豁免原则

**禁止把真实密钥写进仓库**，包括"临时"提交。测试需要假凭证时：

- 用明显的占位值（形如 `{{API_KEY_xxxxxxxx}}` 的占位符不是凭证，不会命中）；
- 或写进 `scripts/privacy-scan.allowlist`，格式为 `<文件><TAB><行文本片段>`，
  命中行含该片段即忽略。表里每一行都必须是人工核对过的非敏感样本，
  **真实密钥绝不入表**。

两处刻意的范围限制，避免误报淹没门禁：

- 本机路径/邮箱只在**非测试文件**里检测（`*_test.go`、`testdata/` 里的假路径
  是测试夹具，不是泄露）；密钥检测仍然覆盖测试文件。
- `private-key` 要求头部后面跟真实密钥体，因此文档或测试里只提到头部不会命中。
