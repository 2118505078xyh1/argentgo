package tools

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// 本文件保存 multi_edit 规划的纯只读层。它不做
// 任何 I/O、不取锁、不写字节。实际的 multi_edit 路径只在
// opt-in 的只读重定位诊断（relocate=true）中查询它，
// 因此默认行为保持逐字节一致。跨文件重定位
// 会改变编辑语义，并可能悄悄编辑模型未指名的文件，
// 这就是实际路径只报告候选而从不自动应用
// 它们的原因。

// multiEditBlock 是供 dry run 使用的单个 SEARCH/REPLACE 块。Path 是模型
// 打算编辑的文件，Old 是 SEARCH 文本，New 是 REPLACE 文本。
// 别名归一化（old_text/new_text）和工作区路径解析仍由
// 调用方工具负责，因此这里仍是纯值对象。
type multiEditBlock struct {
	Path string
	Old  string
	New  string
}

// multiEditKnownFiles 是候选文件的只读快照，以规范路径为键。
// 归一化规则见 canonicalEditPath。
type multiEditKnownFiles map[string]string

// multiEditFileResolver 提供 dry run 所需的快照。Content 返回
// 文件的当前内容以及该文件是否已知；Paths 列出当 SEARCH 块
// 未命中目标时可以考虑的所有文件。
// 实现方不得写磁盘。
type multiEditFileResolver interface {
	Content(path string) (string, bool)
	Paths() []string
}

// multiEditSnapshotResolver 把内存快照适配到 resolver
// 接口。它在测试中很有用，也适用于已经
// 读过候选文件的未来调用方。
type multiEditSnapshotResolver struct {
	files multiEditKnownFiles
}

func (r multiEditSnapshotResolver) Content(path string) (string, bool) {
	content, ok := r.files[canonicalEditPath(path)]
	return content, ok
}

func (r multiEditSnapshotResolver) Paths() []string {
	out := make([]string, 0, len(r.files))
	for path := range r.files {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// canonicalEditPath 使使用斜杠或反斜杠分隔符的调用方之间的
// 路径比较保持稳定。它纯粹是词法层面的：不访问文件系统，
// 因此符号链接和 CJK 名称保持原样（CJK 字节不会被
// Clean/ToSlash 改动）。调用方传入工作区相对路径或已解析的路径。
func canonicalEditPath(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}

// multiEditFileCandidate 是块目标文件之外、在其中找到了
// SEARCH 文本的文件。ViaRepair 表示只有分级修复
// 阶梯匹配：这样的候选在使用前需要显式决策，
// 因为模糊匹配比字面匹配证据更弱。
type multiEditFileCandidate struct {
	Path      string
	ViaRepair bool
}

// multiEditFailureKind 对块为何无法按原样应用进行分类。
type multiEditFailureKind string

const (
	multiEditEmptySearch multiEditFailureKind = "empty_search"
	multiEditNotFound    multiEditFailureKind = "not_found"
	multiEditNotUnique   multiEditFailureKind = "not_unique"
	multiEditNeedsRepair multiEditFailureKind = "needs_repair"
	multiEditWrongFile   multiEditFailureKind = "wrong_file"
)

// multiEditFileFailure 是 dry-run 计划中一个失败的块。Index 是
// 这是输入批次中基于 0 的位置；对 wrong_file 会填充 Candidates
// 以及 needs_repair 失败，并按路径排序以获得确定性输出。
type multiEditBlockFailure struct {
	Index      int
	Kind       multiEditFailureKind
	Path       string
	Candidates []multiEditFileCandidate
}

// multiEditPlan 是 dry run 的结果。Applicable 保存
// 在其自身文件中字面且唯一匹配的块的 0 基索引，
// 按输入顺序排列。Failed 为每个需要决策的块保存一条记录。Snapshot 保存
// 按顺序只应用 Applicable 编辑后的模拟内容；
// 失败的块从不参与模拟，因此后续块看不到任何推测性的
// 重定位或模糊编辑。
type multiEditPlan struct {
	Applicable []int
	Failed     []multiEditBlockFailure
	Snapshot   multiEditKnownFiles
}

// OK 报告是否每个块都能按原样字面应用。
func (p multiEditPlan) OK() bool { return len(p.Failed) == 0 }

// planMultiEdit 在内存中校验整个批次并对每个块分类。
// 它从不写入。各块按顺序针对快照求值，因此
// 成功的编辑对后续块可见（与实际工具的
// 顺序语义一致），失败的编辑则不可见。
//
// 分类有意保持保守：
//   - 目标文件中唯一的字面匹配是 applicable；
//   - 仅在另一个已知文件中唯一的字面匹配是 wrong_file；
//   - 只能通过修复阶梯恢复的匹配是 needs_repair；
//   - 目标文件中有多个字面匹配则是 not_unique（绝不
//     （猜测的结果，与 repairReplace 保持一致）；
//   - 任何地方都没有匹配是 not_found；空的 Old 是 empty_search。
func planMultiEdit(blocks []multiEditBlock, resolver multiEditFileResolver) multiEditPlan {
	plan := multiEditPlan{Snapshot: make(multiEditKnownFiles)}
	for _, path := range resolver.Paths() {
		if content, ok := resolver.Content(path); ok {
			plan.Snapshot[canonicalEditPath(path)] = content
		}
	}

	for i, block := range blocks {
		if block.Old == "" {
			plan.Failed = append(plan.Failed, multiEditBlockFailure{Index: i, Kind: multiEditEmptySearch, Path: block.Path})
			continue
		}

		path := canonicalEditPath(block.Path)
		content, exists := plan.Snapshot[path]
		if exists {
			switch strings.Count(content, block.Old) {
			case 1:
				plan.Applicable = append(plan.Applicable, i)
				plan.Snapshot[path] = strings.Replace(content, block.Old, block.New, 1)
				continue
			case 0:
				// 字面未命中：把可在文件内修复的情况单独记录，
				// 与文件名写错区分开，但未经决策两者都不应用。
				if _, ok := repairReplace(content, block.Old, block.New); ok {
					plan.Failed = append(plan.Failed, multiEditBlockFailure{
						Index: i, Kind: multiEditNeedsRepair, Path: block.Path,
						Candidates: []multiEditFileCandidate{{Path: path, ViaRepair: true}},
					})
					continue
				}
			default:
				plan.Failed = append(plan.Failed, multiEditBlockFailure{Index: i, Kind: multiEditNotUnique, Path: block.Path})
				continue
			}
		}

		candidates := resolveBlockFile(block, plan.Snapshot)
		kind := multiEditNotFound
		for _, c := range candidates {
			if !c.ViaRepair {
				kind = multiEditWrongFile
				break
			}
			kind = multiEditNeedsRepair
		}
		plan.Failed = append(plan.Failed, multiEditBlockFailure{Index: i, Kind: kind, Path: block.Path, Candidates: candidates})
	}
	return plan
}

// resolveBlockFile 在每个已知文件中查找 SEARCH 块（目标文件除外），
// 以此恢复模型写错的文件名。字面唯一匹配优先返回，
// 其后是模糊（修复阶梯）匹配；每组按路径排序，使调用方获得
// 稳定的输出，并可以选择
// 完全忽略较弱的一组。
//
// 它是纯诊断：从不编辑任何内容，当两个文件都匹配时也从不挑选胜者，
// 因为猜测会改错文件。
func resolveBlockFile(block multiEditBlock, knownFiles multiEditKnownFiles) []multiEditFileCandidate {
	if block.Old == "" {
		return nil
	}
	intended := canonicalEditPath(block.Path)
	var exact, fuzzy []multiEditFileCandidate
	for path, content := range knownFiles {
		canon := canonicalEditPath(path)
		if canon == intended {
			continue
		}
		if strings.Count(content, block.Old) == 1 {
			exact = append(exact, multiEditFileCandidate{Path: path})
			continue
		}
		if _, ok := repairReplace(content, block.Old, block.New); ok {
			fuzzy = append(fuzzy, multiEditFileCandidate{Path: path, ViaRepair: true})
		}
	}
	sort.Slice(exact, func(i, j int) bool { return exact[i].Path < exact[j].Path })
	sort.Slice(fuzzy, func(i, j int) bool { return fuzzy[i].Path < fuzzy[j].Path })
	return append(exact, fuzzy...)
}

// multiEditBatchDiagnostic 为失败的批次构建自洽的诊断。
// 指令始终遵循实际写入语义：
//   - atomic（当前的 multi_edit）：没有写入任何字节，因此整个批次
//     必须重新发送；
//   - graded（未来可能的局部写入器）：已写入匹配的编辑，
//     因此只有失败的块才应重新发送。
//
// tools_builtin_write_edit.go 中的 multiEditRepairError 与此处
// 原子分支保持同步；两者都由各自测试逐字节断言，因此
// 措辞更改必须同时落在两处。
func multiEditBatchDiagnostic(path string, failedIdx, total, applied int, content, sample string, atomic bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "multi_edit: repair failed at edit %d of %d after %d edit(s) attempted in %s", failedIdx, total, total, path)
	switch {
	case applied == 0:
		b.WriteString("; no edit matched; re-send the whole batch with the failing edit corrected")
	case atomic:
		fmt.Fprintf(&b, "; %d edit(s) matched cleanly in memory but no bytes were written (atomic batch); re-send the whole batch with the failing edit corrected", applied)
	default:
		fmt.Fprintf(&b, "; %d edit(s) were applied and written; do not re-send them; re-send only the failing edit(s) with the correction", applied)
	}
	if s := formatSimilarLines(content, sample); s != "" {
		b.WriteString("; ")
		b.WriteString(s)
	}
	return b.String()
}
