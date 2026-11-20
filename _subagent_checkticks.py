import re
import sys
file = sys.argv[1]
start = int(sys.argv[2]); end = int(sys.argv[3])
src = open(r'D:/code/mpi/argent-go/_subagent_numbered.txt', encoding='utf-8').read().splitlines()
S = {int(l.split('\t', 1)[0]): l.split('\t', 1)[1] for l in src}
T = {int(l.split('\t', 1)[0]): l.split('\t', 1)[1] for l in open(file, encoding='utf-8').read().splitlines()}
n = 0
for i in sorted(T):
    a = re.findall('`[^`]*`', S[i])
    b = re.findall('`[^`]*`', T[i])
    if a != b:
        print(i, 'SRC', a, 'TR', b)
        print('  S:', S[i][:100])
        print('  T:', T[i][:100])
        n += 1
print('BACKTICK_MISMATCHES:', n)
