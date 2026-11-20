p = r'D:/code/mpi/argent-go/_subagent_indexed_17.txt'
lines = open(p, encoding='utf-8').read().splitlines()
fix = {
    3913: '// 使本包不依赖 agent 或 llm 包。',
    3919: '// HandleUseSkill 用原始 JSON 参数执行 use_skill 工具。',
    3943: '// path.Match 拥有 `*` / `?` / `[...]` 通配符语法。',
    3962: '// 或被匹配参数满足。换句话说，路径、关键词和',
}
out = []
for l in lines:
    n = int(l.split('\t', 1)[0])
    out.append(f'{n}\t{fix[n]}' if n in fix else l)
open(p, 'w', encoding='utf-8').write('\n'.join(out) + '\n')
print('fixed', len(fix))
