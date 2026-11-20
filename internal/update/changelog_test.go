package update

import "testing"

// sampleChangelog 模拟项目根部的 CHANGELOG.md：含中文内容、多个版本段落，
// 以及一个需要在段落内被保留的三级子标题。
const sampleChangelog = `# 更新日志

## v2.0.0 — 2026-02-01

- 全新架构
- 支持插件

## v1.2.0 — 2026-01-15

- 修复中文路径问题
- 提升启动速度

### 内部改动

- 重构下载模块

## v1.0.0 — 2025-12-01

首个正式版本。
`

func TestExtractChangelogSectionExactVersion(t *testing.T) {
	want := "- 修复中文路径问题\n- 提升启动速度\n\n### 内部改动\n\n- 重构下载模块"
	if got := extractChangelogSection(sampleChangelog, "1.2.0"); got != want {
		t.Errorf("extractChangelogSection(1.2.0) = %q, want %q", got, want)
	}
}

func TestExtractChangelogSectionVPrefix(t *testing.T) {
	want := "- 全新架构\n- 支持插件"

	// 传入带 v 的版本号，标题也带 v。
	if got := extractChangelogSection(sampleChangelog, "v2.0.0"); got != want {
		t.Errorf("extractChangelogSection(v2.0.0) = %q, want %q", got, want)
	}

	// 标题不带 v 前缀时，传入带 v 的版本号同样应匹配。
	changelog := "## 3.0.0 — 2026-03-01\n\n- 新功能\n"
	if got := extractChangelogSection(changelog, "v3.0.0"); got != "- 新功能" {
		t.Errorf("extractChangelogSection(标题无 v) = %q, want %q", got, "- 新功能")
	}
}

func TestExtractChangelogSectionCJKContent(t *testing.T) {
	changelog := "## v0.8.0 — 2026-11-20\n\n针对 goose 与 opencode 做了多轮加固。\n\n" +
		"**安全与审批**\n- 凭据脱敏现在对 HTTP 头不区分大小写\n- 路径策略识别 `file_path`\n"
	want := "针对 goose 与 opencode 做了多轮加固。\n\n**安全与审批**\n" +
		"- 凭据脱敏现在对 HTTP 头不区分大小写\n- 路径策略识别 `file_path`"
	if got := extractChangelogSection(changelog, "0.8.0"); got != want {
		t.Errorf("extractChangelogSection(CJK) = %q, want %q", got, want)
	}
}

func TestExtractChangelogSectionNotFound(t *testing.T) {
	for _, version := range []string{"9.9.9", "1.2", "  "} {
		if got := extractChangelogSection(sampleChangelog, version); got != "" {
			t.Errorf("extractChangelogSection(%q) = %q, want empty", version, got)
		}
	}
}

func TestExtractChangelogSectionEmptyChangelog(t *testing.T) {
	for _, changelog := range []string{"", "\n\n", "仅有一段说明，没有版本标题。"} {
		if got := extractChangelogSection(changelog, "1.2.0"); got != "" {
			t.Errorf("extractChangelogSection(空变更日志) = %q, want empty", got)
		}
	}
}

func TestExtractChangelogSectionMultipleVersionBoundaries(t *testing.T) {
	tests := []struct {
		version string
		want    string
	}{
		{"2.0.0", "- 全新架构\n- 支持插件"},
		{"1.2.0", "- 修复中文路径问题\n- 提升启动速度\n\n### 内部改动\n\n- 重构下载模块"},
		{"1.0.0", "首个正式版本。"},
	}
	for _, test := range tests {
		if got := extractChangelogSection(sampleChangelog, test.version); got != test.want {
			t.Errorf("extractChangelogSection(%s) = %q, want %q", test.version, got, test.want)
		}
	}
}

func TestExtractChangelogSectionHandlesCRLFAndPrerelease(t *testing.T) {
	changelog := "## v1.0.0-beta.2 — 2026-01-01\r\n\r\n- 预发布内容\r\n\r\n## v1.0.0 — 2026-01-02\r\n\r\n- 正式内容\r\n"
	if got := extractChangelogSection(changelog, "v1.0.0-beta.2"); got != "- 预发布内容" {
		t.Errorf("预发布段落 = %q, want %q", got, "- 预发布内容")
	}
	if got := extractChangelogSection(changelog, "1.0.0"); got != "- 正式内容" {
		t.Errorf("正式版段落 = %q, want %q", got, "- 正式内容")
	}
}
