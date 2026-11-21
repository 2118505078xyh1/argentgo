package hooks

import (
	"path/filepath"
	"strings"
)

// needsTrustGate 判断一份钩子文档是否需要经过信任门控。
//
// 位于项目根（含其子目录）之下的钩子文档来自工作区，可能随仓库一起被
// 分发，因此必须先通过摘要授权才能运行；用户全局路径属于本机用户自己的
// 配置，不在此列。
//
// 这是纯函数：不做磁盘 I/O，也不修改任何输入，只比较路径文本。比较前会把
// 两条路径规范化为清理后的绝对路径，因此 "./proj/../proj" 与 "proj" 等价；
// 在 Windows 上比较不区分大小写，以匹配文件系统语义。
//
// 为安全处理含中文等 CJK 字符的路径，这里只在清理后的完整路径上追加路径
// 分隔符再比较前缀，绝不对字符串按字节或 rune 下标做切片，因而不会把多字节
// 字符截断在中间。前缀必须以分隔符结尾，避免 "/work/proj2" 被误判为位于
// "/work/proj" 之内。
func needsTrustGate(docPath string, projectRoot string) bool {
	docPath = strings.TrimSpace(docPath)
	projectRoot = strings.TrimSpace(projectRoot)
	if docPath == "" || projectRoot == "" {
		return false
	}

	doc := hookPathAbs(docPath)
	root := hookPathAbs(projectRoot)
	if doc == "" || root == "" {
		return false
	}

	// 根本身也算"在根内"：与包内 withinWorkspace 的约定保持一致。
	if sameHookPath(doc, root) {
		return true
	}

	sep := string(filepath.Separator)
	prefix := root
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	if filepath.Separator == '\\' {
		return strings.HasPrefix(strings.ToLower(doc), strings.ToLower(prefix))
	}
	return strings.HasPrefix(doc, prefix)
}

// hookPathAbs 把 path 转成清理后的绝对路径；转换失败时返回空字符串。
func hookPathAbs(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	return filepath.Clean(abs)
}

// sameHookPath 比较两条已规范化的路径：Windows 上不区分大小写。
func sameHookPath(a, b string) bool {
	if filepath.Separator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}
