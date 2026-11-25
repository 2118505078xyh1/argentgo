package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"example.com/argentgo/internal/security"
)

// 本文件承载 TODO #25：把已批准的作用域回灌到有界 system 提示并随压缩保留。
// 作用域只取工具名或命令前缀，绝不写自由文本参数/反馈文字。持久化真源是
// security 的 approvals.jsonl（APPROVAL_PERSIST 写入），因此每次 Execute 重建
// system 提示都会重新读出同一份作用域——压缩只折叠会话消息，不会抹掉 system。
// 默认开启，ARGENT_APPROVAL_SCOPE_HINT=0 紧急回退。

const (
	approvalScopeHintEnv    = "ARGENT_APPROVAL_SCOPE_HINT"
	approvalScopeHintBudget = 500 // 提示片段的字节上界（对齐条目最优值）
	approvalScopeMaxRunes   = 120 // 单条作用域的最大 rune 数
)

func approvalScopeHintEnabled(env func(string) string) bool {
	if env == nil {
		return true
	}
	return strings.TrimSpace(env(approvalScopeHintEnv)) != "0"
}

// approvalScopeHint 保存本次运行观察到（以及从磁盘读到）的已批准作用域。
// 零值可用；所有方法可被并发调用。
type approvalScopeHint struct {
	mu     sync.RWMutex
	scopes map[string]struct{}
}

func (h *approvalScopeHint) add(scope string) {
	scope = cleanApprovalScope(scope)
	if scope == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.scopes == nil {
		h.scopes = make(map[string]struct{})
	}
	h.scopes[scope] = struct{}{}
}

func (h *approvalScopeHint) snapshot() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.scopes))
	for scope := range h.scopes {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}

// render 生成有界提示：固定头在最前便于回滚时裁剪；按作用域逐行追加，
// 一旦放不下就停止，绝不输出半个作用域或任何参数原文。
func (h *approvalScopeHint) render(maxBytes int) string {
	scopes := h.snapshot()
	if len(scopes) == 0 {
		return ""
	}
	if maxBytes <= 0 {
		maxBytes = approvalScopeHintBudget
	}
	head := "Approved command prefix saved: 以下作用域已获授权，无需重复请求审批（仅作用域，不含参数）："
	lines := []string{head}
	used := len(head)
	for _, scope := range scopes {
		line := "\n- " + scope
		if used+len(line) > maxBytes {
			break
		}
		lines = append(lines, line)
		used += len(line)
	}
	return strings.Join(lines, "")
}

// cleanApprovalScope 收敛作用域字形：单行、无控制字符、无引号/赋值/变量插值，
// 且长度有界。任何被改写的输入直接拒绝，避免把自由文本参数伪装成作用域。
func cleanApprovalScope(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || utf8.RuneCountInString(s) > approvalScopeMaxRunes || len(s) > approvalScopeHintBudget/2 {
		return ""
	}
	if strings.ContainsAny(s, "\r\n\x00\t\"'`=$<>;|&") {
		return ""
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return s
}

// approvalScopeFor 从一次审批调用里提取作用域：非 shell 工具只取工具名；
// bash/powershell 取 command 的前两个安全 token 作为命令前缀（如 "git status"）。
// 解析失败或 token 含可疑字形时回落到工具名，绝不把整条命令或自由文本当作用域。
func approvalScopeFor(name, args string) string {
	if name != "bash" && name != "powershell" {
		return cleanApprovalScope(name)
	}
	var parsed struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(args), &parsed) != nil {
		return cleanApprovalScope(name)
	}
	fields := strings.Fields(parsed.Command)
	if len(fields) == 0 {
		return cleanApprovalScope(name)
	}
	if len(fields) > 2 {
		fields = fields[:2]
	}
	for _, token := range fields {
		if cleanApprovalScope(token) != token {
			return cleanApprovalScope(name)
		}
	}
	return name + ": " + strings.Join(fields, " ")
}

// approvalsGrantedScopes 读取 approvals.jsonl 中所有 kind=always 的干净作用域。
// 文件不存在返回空；单行损坏时跳过该行（security 层已 fail-closed，这里只做展示）。
func approvalsGrantedScopes(storage string) []string {
	if strings.TrimSpace(storage) == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(storage, "approvals.jsonl"))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var grant struct {
			Kind  string `json:"kind"`
			Scope string `json:"scope"`
		}
		if json.Unmarshal(scanner.Bytes(), &grant) != nil || grant.Kind != "always" {
			continue
		}
		scope := cleanApprovalScope(grant.Scope)
		if scope != "" && !seen[scope] {
			seen[scope] = true
			out = append(out, scope)
		}
	}
	return out
}

// approvalScopeSystemHint 返回有界的 system 提示片段；无已批准作用域时为空串。
// 每次调用都重新读盘，使压缩后重建 system 时拿到同一份权威作用域。
func approvalScopeSystemHint(storage string) string {
	hint := &approvalScopeHint{}
	for _, scope := range approvalsGrantedScopes(storage) {
		hint.add(scope)
	}
	return hint.render(approvalScopeHintBudget)
}

// appendApprovalScopeHint 把提示片段追加到 system 末尾。默认开启；无作用域或
// =0 回退时原样返回 system，字节不变。调用方传 nil env 表示使用默认值。
func appendApprovalScopeHint(system, storage string, env func(string) string) string {
	if !approvalScopeHintEnabled(env) {
		return system
	}
	hint := approvalScopeSystemHint(storage)
	if hint == "" {
		return system
	}
	return system + "\n" + hint
}

// approvalGrantObserver 包装审批器：只在 inner 明确授予（granted && err==nil）时
// 记录作用域；拒绝、反馈、基础设施错误都不记。决定原样返回，不改安全语义。
type approvalGrantObserver struct {
	inner security.Approver
	hint  *approvalScopeHint
}

func (o *approvalGrantObserver) Ask(ctx context.Context, name, args string) (bool, error) {
	granted, err := o.inner.Ask(ctx, name, args)
	if granted && err == nil && o.hint != nil {
		o.hint.add(approvalScopeFor(name, args))
	}
	return granted, err
}

// observeApprovalGrants 把授予观察者接到审批器上，供本次运行的提示片段消费。
func observeApprovalGrants(inner security.Approver, hint *approvalScopeHint) security.Approver {
	if inner == nil || hint == nil {
		return inner
	}
	return &approvalGrantObserver{inner: inner, hint: hint}
}
