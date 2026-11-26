package provider_test

import (
	"reflect"
	"testing"

	"example.com/argentgo/internal/provider"
)

func TestExpandConfigValue(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		vars       map[string]string
		lastStatus int
		want       string
	}{
		{
			name:  "空字符串",
			input: "",
			want:  "",
		},
		{
			name:  "无展开内容原样返回",
			input: "hello world /path/to/模型",
			want:  "hello world /path/to/模型",
		},
		{
			name:  "裸美元符号原样返回",
			input: "price is $ and 100$",
			want:  "price is $ and 100$",
		},
		{
			name:  "变量展开",
			input: "$HOME",
			vars:  map[string]string{"HOME": "/home/u"},
			want:  "/home/u",
		},
		{
			name:  "变量展开后接普通字符",
			input: "dir=$HOME/bin",
			vars:  map[string]string{"HOME": "/home/u"},
			want:  "dir=/home/u/bin",
		},
		{
			name:  "花括号变量界定边界",
			input: "${HOME}end",
			vars:  map[string]string{"HOME": "x", "HOMEend": "y"},
			want:  "xend",
		},
		{
			name:  "缺失变量展开为空",
			input: "a${MISSING}b$ALSO_MISSING c",
			want:  "ab c",
		},
		{
			name:  "存在但为空的变量展开为空",
			input: "a$EMPTY b${EMPTY}c",
			vars:  map[string]string{"EMPTY": ""},
			want:  "a bc",
		},
		{
			name:  "默认值在变量缺失时生效",
			input: "${MISSING:-fallback}",
			want:  "fallback",
		},
		{
			name:  "默认值在变量存在时忽略",
			input: "${HOME:-fallback}",
			vars:  map[string]string{"HOME": "/home/u"},
			want:  "/home/u",
		},
		{
			name:  "默认值在变量为空时生效（:- 语义）",
			input: "${EMPTY:-fallback}",
			vars:  map[string]string{"EMPTY": ""},
			want:  "fallback",
		},
		{
			name:  "默认值本身按字面量处理不递归展开",
			input: "${MISSING:-$HOME}",
			vars:  map[string]string{"HOME": "/home/u"},
			want:  "$HOME",
		},
		{
			name:       "上一轮退出码",
			input:      "status=$?",
			lastStatus: 42,
			want:       "status=42",
		},
		{
			name:  "退出码为零",
			input: "$?",
			want:  "0",
		},
		{
			name:       "退出码与变量混合展开",
			input:      "code=$? path=$HOME end=${MISSING:-none}",
			vars:       map[string]string{"HOME": "/tmp"},
			lastStatus: 7,
			want:       "code=7 path=/tmp end=none",
		},
		{
			name:  "转义美元符号",
			input: `\$HOME`,
			vars:  map[string]string{"HOME": "/home/u"},
			want:  "$HOME",
		},
		{
			name:  "转义反引号",
			input: "\\`id\\`",
			want:  "`id`",
		},
		{
			name:  "转义反斜杠后再展开",
			input: "\\\\$HOME",
			vars:  map[string]string{"HOME": "/home/u"},
			want:  "\\/home/u",
		},
		{
			name:  "未知转义序列原样保留",
			input: `\\x\$y\中`,
			vars:  map[string]string{"y": "Y"},
			want:  `\x$y\中`,
		},
		{
			name:  "行尾单独反斜杠原样保留",
			input: "abc\\",
			want:  "abc\\",
		},
		{
			name:  "命令替换不执行原样保留",
			input: "$(whoami)",
			vars:  map[string]string{"whoami": "nobody"},
			want:  "$(whoami)",
		},
		{
			name:  "反引号原样保留不执行",
			input: "prefix `id` suffix",
			want:  "prefix `id` suffix",
		},
		{
			name:  "默认值中的命令替换不执行",
			input: "${MISSING:-$(id)}",
			want:  "$(id)",
		},
		{
			name:  "位置参数不受支持原样保留",
			input: "$1 $2",
			want:  "$1 $2",
		},
		{
			name:  "非 ASCII 变量字符不被当作变量名",
			input: "$中文-bin",
			vars:  map[string]string{"中文": "值"},
			want:  "$中文-bin",
		},
		{
			name:  "不支持的花括号形式原样保留",
			input: "${} ${1} ${A-B} ${UNCLOSED",
			vars:  map[string]string{"A": "x", "UNCLOSED": "y"},
			want:  "${} ${1} ${A-B} ${UNCLOSED",
		},
		{
			name:       "CJK 安全：中文文本与变量共存",
			input:      "目录=$BASE/子目录 状态=$?",
			vars:       map[string]string{"BASE": "/中文"},
			lastStatus: 7,
			want:       "目录=/中文/子目录 状态=7",
		},
		{
			name:  "CJK 变量值原样保留",
			input: "${KEY}",
			vars:  map[string]string{"KEY": "测试用户"},
			want:  "测试用户",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := provider.ExpandCtx{Vars: tt.vars, LastStatus: tt.lastStatus}
			if got := provider.ExpandConfigValue(tt.input, ctx); got != tt.want {
				t.Fatalf("ExpandConfigValue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExpandConfigValueDoesNotMutateInput(t *testing.T) {
	vars := map[string]string{"HOME": "/home/u", "EMPTY": "", "名": "值"}
	original := make(map[string]string, len(vars))
	for k, v := range vars {
		original[k] = v
	}
	input := "$HOME/${MISSING:-x}/$?/\\$HOME/中文"

	ctx := provider.ExpandCtx{Vars: vars, LastStatus: 9}
	first := provider.ExpandConfigValue(input, ctx)
	second := provider.ExpandConfigValue(input, ctx)

	if !reflect.DeepEqual(vars, original) {
		t.Fatalf("ExpandConfigValue 修改了 Vars: got %#v, want %#v", vars, original)
	}
	if first != second {
		t.Fatalf("重复调用结果不一致: first = %q, second = %q", first, second)
	}
	if first == "" {
		t.Fatal("展开结果不应为空")
	}
}

func TestHasExpand(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "空字符串", input: "", want: false},
		{name: "普通文本", input: "hello 中文 /path", want: false},
		{name: "普通反斜杠", input: `C:\Users\xyh`, want: false},
		{name: "美元变量", input: "$HOME", want: true},
		{name: "花括号变量", input: "${HOME}", want: true},
		{name: "退出码", input: "$?", want: true},
		{name: "反引号", input: "`id`", want: true},
		{name: "转义美元仍返回 true（保守快路径）", input: `\$HOME`, want: true},
		{name: "命令替换", input: "$(id)", want: true},
		{name: "CJK 中的美元", input: "价格$PRICE 元", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := provider.HasExpand(tt.input); got != tt.want {
				t.Fatalf("HasExpand(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
