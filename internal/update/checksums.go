package update

import (
	"errors"
	"fmt"
	"strings"
)

// ChecksumEntry 是校验和清单中的一行。Hash 规范化为
// 小写十六进制；Name 是清单中出现的原样文件名，
// 不含前导 "./"。
type ChecksumEntry struct {
	Hash string
	Name string
}

// ParseChecksums 解析由 GNU coreutils 及大多数发布流水线生成的
// checksums.txt / sha256sums.txt 清单。每个非空、
// 非注释行必须使用以下两种规范格式之一：
//
//	<64-hex-digest>  <file name>   （文本模式：两个空格）
//	<64-hex-digest> *<file name>   （二进制模式：一个空格加一个星号）
//
// 文件名前导 "./" 会被去掉，摘要转为小写。
// 遇到第一个格式错误的行时解析停止，并返回指出 1 基行号的
// 错误。这是一个纯函数：不执行任何 I/O，
// 也从不修改输入。
func ParseChecksums(text string) ([]ChecksumEntry, error) {
	entries := make([]ChecksumEntry, 0)
	for number, raw := range strings.Split(text, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		entry, err := parseChecksumLine(trimmed)
		if err != nil {
			return nil, fmt.Errorf("update: checksums line %d: %w", number+1, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// parseChecksumLine 校验并解码一条规范的校验和行。
func parseChecksumLine(line string) (ChecksumEntry, error) {
	// 摘要是第一个以空白分隔的标记，因此分隔符从
	// 第一个空格开始。在那里定位它，而不是先搜索 "  "，
	// 对二进制模式的行很重要：文件名合法地可以包含两个
	// 连续空格（"<hash> *my  file.zip"），而在行中任意位置搜索 "  " 会
	// 在名称内部找到该空格对，并把错误的摘要喂给
	// normaliseChecksum。检查第一个空格之后的字节，恰好接受
	// 两种规范分隔符："  "（文本模式）和 " *"（二进制模式）。
	index := strings.IndexByte(line, ' ')
	if index < 0 || index+1 >= len(line) || (line[index+1] != ' ' && line[index+1] != '*') {
		return ChecksumEntry{}, errors.New("expected two spaces or a space and asterisk after the digest")
	}
	digest, err := normaliseChecksum(line[:index])
	if err != nil {
		return ChecksumEntry{}, fmt.Errorf("invalid digest: %w", err)
	}
	name := strings.TrimSpace(line[index+2:])
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimSpace(name)
	if name == "" {
		return ChecksumEntry{}, errors.New("empty file name")
	}
	return ChecksumEntry{Hash: digest, Name: name}, nil
}

// MatchedChecksum 将发布资源与其对应的清单哈希
// 关联起来。
type MatchedChecksum struct {
	Name string
	Hash string
}

// ChecksumDiagnosis 是将校验和清单与发布资源匹配后的只读结果。
// 所有切片保持首次出现的输入顺序，因此相同输入
// 总是产生相同输出。
type ChecksumDiagnosis struct {
	Matched   []MatchedChecksum // assets with exactly one covering hash
	Missing   []string          // asset names with no covering hash
	Orphans   []string          // manifest names that match no asset
	Conflicts []string          // manifest names carrying two or more distinct hashes
}

// DiagnoseChecksums 将解析后的清单条目与发布资源匹配。
// 名称比较不区分大小写，同名资源以第一个为准。
// 若某条目名称对应多个不同哈希，则为冲突：
// 它会在 Conflicts 中报告，并对其所指资源视为 Missing，
// 因为没有任何单一哈希可以可信地验证该下载。
//
// 该函数是纯函数：既不下载任何内容，也不修改输入，
// 因此可用来在任何资源被获取之前判断已发布的校验和文件是否完整
// 且可信。
func DiagnoseChecksums(entries []ChecksumEntry, assets []Asset) ChecksumDiagnosis {
	var out ChecksumDiagnosis

	assetNames := make(map[string]string, len(assets)) // lowercase -> original
	assetOrder := make([]string, 0, len(assets))
	for _, asset := range assets {
		key := strings.ToLower(strings.TrimSpace(asset.Name))
		if _, seen := assetNames[key]; seen {
			continue
		}
		assetNames[key] = asset.Name
		assetOrder = append(assetOrder, key)
	}

	entryNames := make(map[string]string, len(entries)) // lowercase -> first original
	hashes := make(map[string][]string, len(entries))
	entryOrder := make([]string, 0, len(entries))
	for _, entry := range entries {
		key := strings.ToLower(strings.TrimSpace(entry.Name))
		if _, seen := entryNames[key]; !seen {
			entryNames[key] = entry.Name
			entryOrder = append(entryOrder, key)
		}
		if !containsString(hashes[key], entry.Hash) {
			hashes[key] = append(hashes[key], entry.Hash)
		}
	}

	conflict := make(map[string]bool)
	for _, key := range entryOrder {
		if len(hashes[key]) > 1 {
			conflict[key] = true
			out.Conflicts = append(out.Conflicts, entryNames[key])
		}
	}
	for _, key := range assetOrder {
		name := assetNames[key]
		if conflict[key] || len(hashes[key]) == 0 {
			out.Missing = append(out.Missing, name)
			continue
		}
		out.Matched = append(out.Matched, MatchedChecksum{Name: name, Hash: hashes[key][0]})
	}
	for _, key := range entryOrder {
		if _, exists := assetNames[key]; !exists {
			out.Orphans = append(out.Orphans, entryNames[key])
		}
	}
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
