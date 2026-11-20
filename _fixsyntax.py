import io
p = r'D:\code\mpi\argENT-go\_tr_lines.py'
lines = io.open(p, encoding='utf-8').read().split('\n')
for i, l in enumerate(lines, 1):
    if l.startswith(' "') and l.rstrip().endswith('",'):
        print(i, repr(l[-45:]))
