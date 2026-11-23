package bash

import (
	"errors"
	"fmt"
	"os"
)

// 本文件给内建 shell 的文件读点加统一的有界读取，防止模型对 1GB 日志跑
// cat/head/tail/grep 时让「文件 + 字符串副本 + 行切片」三份驻留触发 OOM。
// 它是 tools.readFileLimited 的本地镜像（tools → bash 是单向依赖，bash 不能
// 反向 import tools；为省十几行引入新包违背"零新增模块"约束，故各留一份）。

// bashReadMaxBytes 是内建 shell 单文件读入的上限，与 read 工具一致（10MB）。
const bashReadMaxBytes = 10 * 1024 * 1024

// errBashFileTooLarge 在文件超过 bash 读入上限时返回，供 errors.Is 匹配。
// 错误信息给出替代命令，让模型改用分页/搜索而非整文件读入。
var errBashFileTooLarge = errors.New("file too large: use sed -n 'START,ENDP' to page or grep to search")

// readFileLimited 在读入前检查文件大小，超限直接返回 errBashFileTooLarge。
// maxBytes 由测试注入小值以避免真的创建 10MB 文件。Stat 失败时交给 os.ReadFile
// 走原有错误路径（保持缺失/权限错误的既有语义）。
func readFileLimited(path string, maxBytes int64) ([]byte, error) {
	if info, err := os.Stat(path); err == nil && info.Size() > maxBytes {
		return nil, fmt.Errorf("%s: %w (%d bytes > %d limit)", path, errBashFileTooLarge, info.Size(), maxBytes)
	}
	return os.ReadFile(path)
}
