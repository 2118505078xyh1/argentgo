import io
p = r'D:\code\mpi\argENT-go\_tr_lines.py'
lines = io.open(p, encoding='utf-8').read().split('\n')
out = []
for l in lines:
    if l.startswith(' "') and l.rstrip().endswith('",'):
        l = l.rstrip()[:-1] + '),'
    out.append(l)
io.open(p, 'w', encoding='utf-8', newline='').write('\n'.join(out))
print('done')
