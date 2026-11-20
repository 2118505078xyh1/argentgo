p = r'D:/code/mpi/argent-go/_subagent_indexed_16.txt'
lines = open(p, encoding='utf-8').read().splitlines()
fix = {
    3702: '// 像 `<file>` 这样的普通占位符描述开放取值且不产生',
    3754: '// 带引号的标量、`key: [a, b]` 内联列表、缩进的 `- item` 列表以及',
    3758: '// ParseFields 解析两个 `---` 栅栏之间的文本。',
    3765: '// List 返回 key 的值。标量 `a, b` 按逗号拆分，因此',
    3769: '// blockText 渲染 `>`、`|` 或普通缩进块。末尾空行',
    3773: '// `\\n` 在 `\\\\` 之前，把字面量 `\\\\n`（反斜杠 + "n"）变成',
    3853: '// 文件以 `---` frontmatter 块开头，至少携带 `name`',
    3855: '// 系统提示中只放紧凑索引（每个技能一行）；正文通过 use_skill 工具获取。二十个安装技能则',
    3878: '// Parse 读取一个技能文档。path 用于诊断并作为回退名称（目录名）。',
}
out = []
for l in lines:
    n = int(l.split('\t', 1)[0])
    out.append(f'{n}\t{fix[n]}' if n in fix else l)
open(p, 'w', encoding='utf-8').write('\n'.join(out) + '\n')
print('fixed')
