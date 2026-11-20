package app

import "sort"

// reviewDiffPlan 根据仓库是否已有提交，规划 /review 审查所需的 git diff 命令序列。
// 返回的每条命令都是不含可执行文件名 git 的参数切片，执行层可直接
// runner.Run("git", args...) 调用。
//
// 有提交时只需一条 `git diff HEAD -- <files>`：HEAD 对比同时覆盖暂存与
// 未暂存的改动；无提交（空仓库）时 HEAD 不存在，改用两条命令——先用
// `git diff --cached -- <files>` 对比暂存区，再用 `git diff -- <files>`
// 对比工作区，两者合起来覆盖全部改动。
//
// changedFiles 会被去重并按字典序排序，且不会修改调用方传入的切片。路径
// 原样透传（不做引号包裹或转义）：命令经 exec 参数传递而非 shell，因此
// CJK 等 UTF-8 路径可安全使用；末尾的 "--" 分隔符则避免以 '-' 开头的
// 文件名被 git 当作选项。
func reviewDiffPlan(hasCommits bool, changedFiles []string) [][]string {
	files := reviewSortedUnique(changedFiles)
	if hasCommits {
		return [][]string{reviewDiffArgs("HEAD", files)}
	}
	return [][]string{
		reviewDiffArgs("--cached", files),
		reviewDiffArgs("", files),
	}
}

// reviewDiffArgs 构造一条 git diff 的参数切片。target 为空时省略对比目标，
// 表示只比较工作区；参数始终以 "--" 结束选项部分，其后均为文件路径。
func reviewDiffArgs(target string, files []string) []string {
	args := []string{"diff"}
	if target != "" {
		args = append(args, target)
	}
	args = append(args, "--")
	return append(args, files...)
}

// reviewSortedUnique 返回 changedFiles 去重并按字典序排序后的副本，不修改入参。
func reviewSortedUnique(changedFiles []string) []string {
	seen := make(map[string]struct{}, len(changedFiles))
	files := make([]string, 0, len(changedFiles))
	for _, file := range changedFiles {
		if _, ok := seen[file]; ok {
			continue
		}
		seen[file] = struct{}{}
		files = append(files, file)
	}
	sort.Strings(files)
	return files
}
