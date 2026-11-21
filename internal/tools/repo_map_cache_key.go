package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// repoMapCacheVersion 是 repo map 缓存条目的格式版本标记。
// 它参与缓存键摘要：当缓存条目的内容格式或输入组合方式发生变化时，
// 只需改动此常量，所有旧键便会因摘要不同而自动失效，
// 无需手动清理进程内缓存或等待文件戳改变。
const repoMapCacheVersion = "v1"

// repoMapCacheKey 为一份 repo map 缓存条目生成稳定且确定性的键，
// 返回 sha256 摘要的前 16 个小写十六进制字符。
//
// 拼接采用「字节长度前缀 + 原始字节」的帧格式：先写字段的字节数，
// 再写字段本身。这样即使字段内容中出现分隔符、NUL 或 CJK 等多字节
// 字符，也不会让字段边界产生歧义或相互碰撞；空字符串被稳定地编码为
// 长度 0。版本常量作为第一个字段参与摘要，因此格式升级后旧键自然失效。
func repoMapCacheKey(root, filesHash, mentions string) string {
	var b strings.Builder
	for _, field := range [...]string{repoMapCacheVersion, root, filesHash, mentions} {
		b.WriteString(strconv.Itoa(len(field)))
		b.WriteByte(':')
		b.WriteString(field)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:16]
}
