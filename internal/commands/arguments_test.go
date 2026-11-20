package commands

import "testing"

// TestExpandArgumentsPureFunction 锁定共享纯函数的占位符展开规则：
// 命名占位符取完整参数串，位置占位符按引号分组取值，越界取空。
func TestExpandArgumentsPureFunction(t *testing.T) {
	cases := []struct {
		name string
		body string
		args string
		want string
	}{
		{
			name: "empty arguments clear placeholders",
			body: "x$1y$ARGUMENTSz",
			args: "",
			want: "xyz",
		},
		{
			name: "named arguments expand to raw string",
			body: "all=$ARGUMENTS at=$@",
			args: "alpha beta",
			want: "all=alpha beta at=alpha beta",
		},
		{
			name: "positional arguments",
			body: "$1 / $2 / $3",
			args: "alpha beta gamma",
			want: "alpha / beta / gamma",
		},
		{
			name: "braced positional argument",
			body: "pre${1}post and ${2}",
			args: "alpha beta",
			want: "prealphapost and beta",
		},
		{
			name: "quoted argument is one position",
			body: "first=$1 second=$2",
			args: `"two words" beta`,
			want: "first=two words second=beta",
		},
		{
			name: "single quoted cjk argument",
			body: "目标=$1 动作=$2",
			args: "'中文 参数' 部署",
			want: "目标=中文 参数 动作=部署",
		},
		{
			name: "out of range position becomes empty",
			body: "x$1|$3|$9|y",
			args: "only",
			want: "xonly|||y",
		},
		{
			name: "highest position does not absorb remainder by default",
			body: "$1 $2",
			args: "alpha beta gamma",
			want: "alpha beta",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExpandArguments(tc.body, tc.args); got != tc.want {
				t.Errorf("ExpandArguments(%q, %q) = %q, want %q", tc.body, tc.args, got, tc.want)
			}
		})
	}
}
