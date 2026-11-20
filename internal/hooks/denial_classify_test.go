package hooks

import "testing"

// TestClassifyDenialReasonByCategory 覆盖各分类的典型英文原因，
// 校验原因被归到稳定的结构化标签而非原样回显。
func TestClassifyDenialReasonByCategory(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		want   string
	}{
		// 策略拒绝：配置的规则、黑白名单、权限表。
		{name: "policy denied by policy", reason: `the tool "bash" is denied by policy`, want: denialCategoryPolicy},
		{name: "policy allow list", reason: `the tool "read" is not on the allow list`, want: denialCategoryPolicy},
		{name: "policy permissions table", reason: "the bash call is denied by the permissions table", want: denialCategoryPolicy},
		{name: "policy rule", reason: `the command is denied by policy (rule "git push --force")`, want: denialCategoryPolicy},
		// 条件拒绝：工作区边界、只读、需要批准。
		{name: "condition workspace escape", reason: `the path "../etc/passwd" escapes the workspace root "/app"`, want: denialCategoryCondition},
		{name: "condition not writable", reason: `the path "/etc/hosts" is not writable: outside write roots`, want: denialCategoryCondition},
		{name: "condition needs approval", reason: `the mutating tool "write" needs operator approval`, want: denialCategoryCondition},
		{name: "condition outside allowed", reason: `the path "/tmp/x" is outside the allowed set`, want: denialCategoryCondition},
		// 引擎故障：无法执行、不可用、解析失败。
		{name: "engine unavailable", reason: "the approval channel is unavailable", want: denialCategoryEngine},
		{name: "engine canonicalize failure", reason: "the path could not be canonicalized for policy checking", want: denialCategoryEngine},
		{name: "engine bad json args", reason: "the bash arguments are not a JSON object: unexpected end", want: denialCategoryEngine},
		{name: "engine timeout", reason: "the hook timed out after 30s", want: denialCategoryEngine},
		// 用户拒绝：操作员否决或批准未授予。
		{name: "user refused", reason: "the operator refused the tool call", want: denialCategoryUser},
		{name: "user rejected with feedback", reason: "the operator rejected the tool call with feedback", want: denialCategoryUser},
		{name: "user not granted", reason: "approval was not granted", want: denialCategoryUser},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDenialReason(tc.reason); got != tc.want {
				t.Fatalf("classifyDenialReason(%q) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

// TestClassifyDenialReasonCJK 校验中文原因按语义关键词归类，
// 且多字节字符不会在匹配过程中被拆开或产生乱码。
func TestClassifyDenialReasonCJK(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		want   string
	}{
		{name: "policy", reason: "该工具被策略禁止", want: denialCategoryPolicy},
		{name: "policy rule", reason: "命中权限表规则：禁止执行", want: denialCategoryPolicy},
		{name: "condition", reason: "路径越界，已超出工作区边界", want: denialCategoryCondition},
		{name: "condition readonly", reason: "目标为只读文件，不可写", want: denialCategoryCondition},
		{name: "condition approval", reason: "该操作需要操作员批准", want: denialCategoryCondition},
		{name: "engine", reason: "审批通道不可用", want: denialCategoryEngine},
		{name: "engine failure", reason: "引擎故障：无法解析参数", want: denialCategoryEngine},
		{name: "user", reason: "操作员拒绝该工具调用", want: denialCategoryUser},
		{name: "user veto", reason: "用户否决", want: denialCategoryUser},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDenialReason(tc.reason); got != tc.want {
				t.Fatalf("classifyDenialReason(%q) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

// TestClassifyDenialReasonUnknownAndEmpty 校验空值、纯空白与无法识别的
// 原因都稳定落到 "unknown"。
func TestClassifyDenialReasonUnknownAndEmpty(t *testing.T) {
	cases := []struct {
		name   string
		reason string
	}{
		{name: "empty", reason: ""},
		{name: "whitespace", reason: "   \t\n "},
		{name: "unrecognized", reason: "something odd happened"},
		{name: "unrecognized cjk", reason: "不太好归类的一段话"},
		{name: "invalid utf8", reason: string([]byte{0xff, 0xfe, 0x80})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDenialReason(tc.reason); got != denialCategoryUnknown {
				t.Fatalf("classifyDenialReason(%q) = %q, want %q", tc.reason, got, denialCategoryUnknown)
			}
		})
	}
}

// TestClassifyDenialReasonCaseInsensitive 校验英文原因大小写不敏感，
// 且首尾空白被忽略。
func TestClassifyDenialReasonCaseInsensitive(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		want   string
	}{
		{name: "upper policy", reason: "  THE TOOL IS DENIED BY POLICY  ", want: denialCategoryPolicy},
		{name: "mixed user", reason: "Operator Refused The Tool Call", want: denialCategoryUser},
		{name: "mixed engine", reason: "The Approval Channel Is UNAVAILABLE", want: denialCategoryEngine},
		{name: "mixed condition", reason: "The Path ESCAPES THE WORKSPACE ROOT", want: denialCategoryCondition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDenialReason(tc.reason); got != tc.want {
				t.Fatalf("classifyDenialReason(%q) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

// TestClassifyDenialReasonPriorityAndDeterminism 校验多关键词命中时按
// 固定优先级裁决（引擎故障优先于策略），且相同输入始终返回相同标签。
func TestClassifyDenialReasonPriorityAndDeterminism(t *testing.T) {
	// 同时包含引擎关键词 "could not" 与策略关键词 "policy"，
	// 按优先级应判为引擎故障。
	reason := "the path could not be canonicalized for policy checking"
	if got := classifyDenialReason(reason); got != denialCategoryEngine {
		t.Fatalf("含引擎与策略双重关键词时应判为 engine, got %q", got)
	}
	// "denied by policy" 不应因泛化的 "denied" 被误判为用户否决。
	if got := classifyDenialReason("the tool is denied by policy"); got != denialCategoryPolicy {
		t.Fatalf("策略拒绝不应被误判为 user, got %q", got)
	}
	for i := 0; i < 3; i++ {
		if got := classifyDenialReason(reason); got != denialCategoryEngine {
			t.Fatalf("第 %d 次调用结果漂移: %q", i, got)
		}
	}
}

// TestClassifyDenialReasonExportedMatchesInternal 校验导出接缝与内部纯函数
// 行为一致，使包外 opt-in 调用得到相同分类。
func TestClassifyDenialReasonExportedMatchesInternal(t *testing.T) {
	for _, reason := range []string{"denied by policy", "操作员拒绝", "", "nothing recognizable"} {
		if got, want := ClassifyDenialReason(reason), classifyDenialReason(reason); got != want {
			t.Fatalf("ClassifyDenialReason(%q) = %q, 内部函数 = %q", reason, got, want)
		}
	}
}
