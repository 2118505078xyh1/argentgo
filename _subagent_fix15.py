p = r'D:/code/mpi/argent-go/_subagent_indexed_15.txt'
lines = open(p, encoding='utf-8').read().splitlines()
fix = {
    3407: '// 列表 `a,b,c` 和带步长的范围 `a-b/n`。星期 0 是星期日，7 也',
    3459: '// TestDaemonDoesNotRerunSlotHandledByAnotherProcess 守护共享 store 的',
}
out = []
for l in lines:
    n = int(l.split('\t', 1)[0])
    out.append(f'{n}\t{fix[n]}' if n in fix else l)
open(p, 'w', encoding='utf-8').write('\n'.join(out) + '\n')
print('fixed')
