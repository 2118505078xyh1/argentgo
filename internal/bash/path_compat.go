package bash

import (
	"runtime"
	"strings"
)

// normalizeMSYSPath 在 Windows 上把 Git Bash / MSYS 风格的路径规范化成盘符路径，
// 例如 "/d/work" → "D:/work"、"/d" → "D:/"。
//
// 背景：模型按 Unix shell 习惯写出的绝对路径常是 "/d/..." 形式（这在 Git Bash
// 里完全合法），但 filepath.IsAbs 在 Windows 上因为没有盘符而判定它为相对路径，
// 于是它被拼到工作区后面，最终报出 "cd: not a directory"。内建 bash 既然要兼容
// shell 的路径写法，就在解析入口统一做这层映射，让边界检查仍按真实盘符路径进行。
//
// 非 Windows 平台原样返回：POSIX 下 "/d/..." 本身就是合法的绝对路径，无需转换。
// 这不是一个开关：它是路径解析的正确性修复，不提供 =0 回退。
func normalizeMSYSPath(p string) string {
	if runtime.GOOS != "windows" || len(p) < 2 || p[0] != '/' {
		return p
	}
	drive := p[1]
	if (drive < 'a' || drive > 'z') && (drive < 'A' || drive > 'Z') {
		return p
	}
	// "/d" 单独出现代表盘符根；"/d/foo" 代表盘符下的路径；"/dx/..." 不是盘符写法。
	if len(p) == 2 {
		return strings.ToUpper(string(drive)) + ":/"
	}
	if p[2] != '/' {
		return p
	}
	return strings.ToUpper(string(drive)) + ":" + p[2:]
}
