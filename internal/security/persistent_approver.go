package security

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// persistentApprovalsFile 是存储目录内保存作用域规则的文件名。
	// 跨进程锁的 sidecar 文件名由数据文件名派生（数据文件名 + ".lock"）。
	persistentApprovalsFile = "approvals.jsonl"
	// persistentGrantVersion 是记录版本号，便于日后格式迁移时 fail-closed。
	persistentGrantVersion = 1
	// maxPersistentGrants 限制磁盘与内存中的规则条数，防止无界增长。
	maxPersistentGrants = 1000
)

// errPersistentUntrusted 在规则文件无法被信任时返回。设置为失败态后，
// 审批器不再兑现任何规则，也不再写入，直到显式重建。
var errPersistentUntrusted = errors.New("security: persistent approval rules untrusted")

// persistentApproverSymbol 是构造子的稳定标识，用于错误上下文与按名接线。
// archguard 把字符串字面量视为反射/RPC 注册点；父层 internal/app 将按此名接线。
const persistentApproverSymbol = "NewFileBackedScopedApprover"

// persistentGrant 是 approvals.jsonl 的一行。Kind 仅取 "always" / "deny"；
// Scope 仅是工具名或调用方给定的命令前缀，绝不保存自由文本参数或反馈文本。
type persistentGrant struct {
	V     int    `json:"v"`
	Kind  string `json:"kind"`
	Scope string `json:"scope"`
	TS    int64  `json:"ts"`
}

// FileBackedScopedApprover 在 inner 审批器之上叠加可跨重启存活的"作用域"规则：
// 放行记住作用域（工具名或命令前缀），拒绝记为粘性 deny。规则以 JSONL 追加在
// <storageDir>/approvals.jsonl，只写作用域，不写参数或反馈。文件损坏时失败即拒绝：
// 既不兑现任何规则，也不再写入。它是纯 opt-in 构件，默认不会被任何人构造，
// 因此默认路径零文件 I/O、行为逐字节不变。
type FileBackedScopedApprover struct {
	mu        sync.Mutex
	inner     Approver
	path      string
	scopeFor  func(name, args string) string
	scopesFor func(name, args string) []string
	always    map[string]bool
	deny      map[string]bool
	failed    bool
	issues    []PolicyIssue
}

// NewFileBackedScopedApprover 包装 inner，并从 storageDir 加载已持久化的作用域规则。
// storageDir 为空时退化为纯内存（零文件 I/O）。scopeFor 为 nil 时作用域取工具名；
// 非 nil 时可返回受限的命令前缀，但绝不能返回自由文本参数——返回非法值会回落到工具名。
func NewFileBackedScopedApprover(inner Approver, storageDir string, scopeFor func(name, args string) string) *FileBackedScopedApprover {
	if inner == nil {
		inner = NeverApprover{}
	}
	f := &FileBackedScopedApprover{
		inner:    inner,
		scopeFor: scopeFor,
		always:   make(map[string]bool),
		deny:     make(map[string]bool),
	}
	if storageDir != "" {
		f.path = filepath.Join(storageDir, persistentApprovalsFile)
	}
	f.load()
	return f
}

// NewFileBackedScopedApproverHierarchical 与 NewFileBackedScopedApprover 相同，
// 但用 scopesFor 为每次调用生成一组作用域（如 bash 的分层通配前缀）。命中任意
// 已授予/已拒绝作用域即生效；放行/拒绝时把整组作用域一并落盘。scopesFor 返回
// 空时回落到"工具名"单值，与旧构造器一致。
func NewFileBackedScopedApproverHierarchical(inner Approver, storageDir string, scopesFor func(name, args string) []string) *FileBackedScopedApprover {
	if inner == nil {
		inner = NeverApprover{}
	}
	f := &FileBackedScopedApprover{
		inner:     inner,
		scopesFor: scopesFor,
		always:    make(map[string]bool),
		deny:      make(map[string]bool),
	}
	if storageDir != "" {
		f.path = filepath.Join(storageDir, persistentApprovalsFile)
	}
	f.load()
	return f
}

// InteractiveReply 把可交互性委托给 inner：包装器不改变回答来源（#23）。
func (f *FileBackedScopedApprover) InteractiveReply() bool {
	if f == nil {
		return false
	}
	return approverAllowsInteractiveReply(f.inner)
}

// Ask 按 deny → always → inner 的顺序决策。inner 的回答经 ParseApproverResult
// 归一化后才落到持久化层：放行记住作用域，无反馈的硬否决记为粘性 deny，
// 带反馈的拒绝（FeedbackRefusal）不落盘——自由文本绝不写入规则文件，原样交回链处理。
func (f *FileBackedScopedApprover) Ask(ctx context.Context, name, args string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	scopes := f.scopesOf(name, args)
	f.mu.Lock()
	if f.failed {
		detail := ""
		if n := len(f.issues); n > 0 {
			detail = fmt.Sprintf(" (%d invalid rule(s))", n)
		}
		f.mu.Unlock()
		return false, fmt.Errorf("security: %s: %w%s", persistentApproverSymbol, errPersistentUntrusted, detail)
	}
	singleUse := singleUseFromContext(ctx)
	for _, scope := range scopes {
		if f.deny[scope] {
			f.mu.Unlock()
			return false, nil
		}
	}
	if !singleUse {
		for _, scope := range scopes {
			if f.always[scope] {
				f.mu.Unlock()
				return true, nil
			}
		}
	}
	f.mu.Unlock()

	granted, err := f.inner.Ask(ctx, name, args)
	ans := ParseApproverResult(granted, err)
	switch persistentKindForFeedback(ans) {
	case "always":
		if err == nil {
			// 敏感调用（配置/说明文件编辑）的 always 只记一次性：不落盘、不进内存。
			if !singleUse {
				for _, scope := range scopes {
					f.GrantAlways(scope)
				}
			}
			return true, nil
		}
	case "deny":
		if err == nil {
			for _, scope := range scopes {
				f.Deny(scope)
			}
			return false, nil
		}
	}
	// 带反馈的拒绝：原样返回 FeedbackRefusal，链会转成模型可读的可恢复错误；
	// 基础设施错误（取消、不可用等）也在此透传，绝不被误记为粘性 deny。
	return false, err
}

// GrantAlways 永久记住一个作用域，并追加到规则文件。粘性 deny 优先：
// 已被拒绝的作用域不会被授予覆盖，也不会写入相互矛盾的规则。
func (f *FileBackedScopedApprover) GrantAlways(scope string) {
	scope = cleanPersistentScope(scope)
	if scope == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed || f.deny[scope] || f.always[scope] {
		return
	}
	f.always[scope] = true
	f.appendGrant(persistentGrant{V: persistentGrantVersion, Kind: "always", Scope: scope, TS: time.Now().Unix()})
}

// Deny 记录对一个作用域的粘性拒绝，并追加到规则文件。拒绝优先于任何授予：
// 内存中清除对应的 always，使后续 Ask 立即走拒绝分支。
func (f *FileBackedScopedApprover) Deny(scope string) {
	scope = cleanPersistentScope(scope)
	if scope == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed || f.deny[scope] {
		return
	}
	delete(f.always, scope)
	f.deny[scope] = true
	f.appendGrant(persistentGrant{V: persistentGrantVersion, Kind: "deny", Scope: scope, TS: time.Now().Unix()})
}

// load 读取规则文件。任何无法解析或语义非法的记录都会把审批器置为失败态 ——
// 这是 fail-closed：宁可拒绝一切，也不在文件被篡改/截断后错误地放行。
// 语义问题同时记入 PolicyIssue，供失败时的诊断信息引用。
func (f *FileBackedScopedApprover) load() {
	if f.path == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		f.failed = true
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	always := make(map[string]bool)
	deny := make(map[string]bool)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var g persistentGrant
		if err := json.Unmarshal([]byte(line), &g); err != nil || !validPersistentGrant(g) {
			f.issues = append(f.issues, PolicyIssue{
				Kind: "invalid_grant", Index: lineNo, Raw: line,
				Reason: "malformed or unsupported persistent approval rule",
			})
			f.failed = true
			return
		}
		switch g.Kind {
		case "always":
			always[g.Scope] = true
		case "deny":
			deny[g.Scope] = true
		}
	}
	if err := scanner.Err(); err != nil || len(always)+len(deny) > maxPersistentGrants {
		f.failed = true
		return
	}
	f.always, f.deny = always, deny
}

// appendGrant 在跨进程锁下把一条规则追加到 JSONL 文件。写入失败被刻意吞掉：
// 内存中的决定对本进程仍然生效，且下一进程会重新询问，不会出现静默放行。
// 调用方须持有 f.mu。
func (f *FileBackedScopedApprover) appendGrant(g persistentGrant) {
	if f.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return
	}
	lk, err := ttlAcquireLock(f.path + ".lock")
	if err != nil {
		return
	}
	defer lk.Release()
	line, err := json.Marshal(g)
	if err != nil {
		return
	}
	fh, err := os.OpenFile(f.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer fh.Close()
	if _, err := fh.Write(append(line, '\n')); err != nil {
		return
	}
	_ = fh.Sync()
}

// scopeOf 计算本次调用的作用域：优先使用注入的提取器并清洗，
// 非法回落到工具名。工具名本身非法时返回空串（不会持久化任何规则）。
func (f *FileBackedScopedApprover) scopeOf(name, args string) string {
	if f.scopeFor != nil {
		if s := cleanPersistentScope(f.scopeFor(name, args)); s != "" {
			return s
		}
	}
	return cleanPersistentScope(name)
}

// scopesOf 计算本次调用的一组作用域。分层作用域优先（#27）；结果经清洗、
// 去重并保持顺序，全空时回落到单值 scopeOf，与旧构造器行为一致。
func (f *FileBackedScopedApprover) scopesOf(name, args string) []string {
	if f.scopesFor != nil {
		seen := make(map[string]bool)
		out := make([]string, 0, 4)
		for _, raw := range f.scopesFor(name, args) {
			scope := cleanPersistentScope(raw)
			if scope == "" || seen[scope] {
				continue
			}
			seen[scope] = true
			out = append(out, scope)
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{f.scopeOf(name, args)}
}

// validPersistentGrant 校验一条记录是否可信：版本、类别、作用域都必须合法。
// 作用域必须是"已经干净"的形式——不就地清洗，避免把被篡改的文本当作规则。
func validPersistentGrant(g persistentGrant) bool {
	if g.V != persistentGrantVersion {
		return false
	}
	if g.Kind != "always" && g.Kind != "deny" {
		return false
	}
	return cleanPersistentScope(g.Scope) == g.Scope
}

// cleanPersistentScope 返回可作为规则键的安全作用域：工具名或命令前缀。
// 复用既有的脱敏/不可见字符清洗，任何被改写的输入都直接拒绝（返回空串）。
func cleanPersistentScope(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxTTLReadableRunes {
		return ""
	}
	clean, hadTags := SanitizeInvisible(s)
	if hadTags || clean != s {
		return ""
	}
	if ttlSafeContext(s) != s {
		return ""
	}
	return s
}
