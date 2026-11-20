p = r'D:/code/mpi/argent-go/_subagent_indexed_14.txt'
lines = open(p, encoding='utf-8').read().splitlines()
fix = {
    3114: '// ExpiringTokens 列出其存储凭据即将',
    3115: '// 在接下来 `within` 窗口内过期的 provider ID，包括',
    3116: '// 已经过期的凭据。这是只读诊断辅助：结果只包含',
    3117: '// provider ID，绝不包含访问或刷新秘密，因此可安全记录、显示于',
    3118: '// UI 或放入支持包。',
    3119: '// 凭据剩余生命周期至多为某窗口时算作即将过期；',
    3120: '// `within`；因此非正的 `within` 把扫描收窄到在',
    3121: '// `now` 时已过期的凭据。负的 `within` 与零相同。',
    3122: '// 没有截止时间的条目（API key）和没有秘密的条目被跳过，',
    3123: '// 因为它们永不过期。`now` 是显式的，使调用方和测试共享一个',
    3182: '// 先应用 provider extras，使它们能提供可选',
    3183: '// 参数（prompt、access_type 等），但协议关键值',
    3184: '// 始终胜出。让 Extra 条目覆盖 `state` 会破坏',
    3185: '// Callback.Wait 中的 CSRF 检查；覆盖 PKCE challenge 或 method',
    3186: '// 会使 Exchange 发送的 verifier 在令牌端点失败。',
}
out = []
for l in lines:
    n = int(l.split('\t', 1)[0])
    out.append(f'{n}\t{fix[n]}' if n in fix else l)
open(p, 'w', encoding='utf-8').write('\n'.join(out) + '\n')
print('fixed', len(fix))
