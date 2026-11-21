package app

import "strconv"

// gitStatusLargeRepoFiles 是区分大型仓库的文件数阈值。
// 文件数超过该阈值时改用精简措辞，避免把仓库规模细节塞进模型上下文。
const gitStatusLargeRepoFiles = 1000

// gitStatusHint 生成一段中文仓库状态提示，供注入模型上下文，帮助模型在脏工作区
// 中优先查看差异、在大型仓库中避免盲目遍历。参数含义：
//   - isRepo：工作区是否为 git 仓库；false 时直接返回空串。
//   - dirtyFiles：未提交改动的文件数；不大于 0 表示工作区干净，返回空串。
//   - fileCount：仓库文件总数；超过 gitStatusLargeRepoFiles 时使用精简措辞；
//     不大于 0 表示规模未知，此时不展示文件总数。
//
// 函数只由固定中文字符串字面量与 strconv.Itoa 拼接而成，不做任何字节级截断，
// 因此结果始终是合法 UTF-8（中文不会被切在字符中间）；字符串长度仅随整数位数
// 增长，存在上界；对相同输入逐字节稳定，可安全地用于缓存稳定的提示前缀。
func gitStatusHint(isRepo bool, dirtyFiles int, fileCount int) string {
	if !isRepo || dirtyFiles <= 0 {
		return ""
	}
	n := strconv.Itoa(dirtyFiles)
	if fileCount > gitStatusLargeRepoFiles {
		return "工作区有 " + n + " 个未提交改动（大型仓库）。"
	}
	if fileCount > 0 {
		return "工作区有 " + n + " 个未提交改动（仓库共 " + strconv.Itoa(fileCount) + " 个文件）。"
	}
	return "工作区有 " + n + " 个未提交改动。"
}
