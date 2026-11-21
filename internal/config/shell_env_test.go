package config

import (
	"reflect"
	"testing"
)

// TestModelFacingEnvValues 校验注入表的键与规定值固定不变。
func TestModelFacingEnvValues(t *testing.T) {
	want := map[string]string{
		"NO_COLOR":  "1",
		"TERM":      "dumb",
		"PAGER":     "cat",
		"GIT_PAGER": "cat",
	}
	if !reflect.DeepEqual(ModelFacingEnv, want) {
		t.Fatalf("ModelFacingEnv = %v, want %v", ModelFacingEnv, want)
	}
}

// TestReservedKeys 校验 ReservedKeys 恰好返回受管键集合，并且返回的是
// 独立副本：修改它不会影响后续调用（只读约定不被外部破坏）。
func TestReservedKeys(t *testing.T) {
	got := ReservedKeys()
	for key := range ModelFacingEnv {
		if _, ok := got[key]; !ok {
			t.Errorf("ReservedKeys() 缺少受管键 %q", key)
		}
	}
	if len(got) != len(ModelFacingEnv) {
		t.Fatalf("ReservedKeys() 长度 = %d, want %d", len(got), len(ModelFacingEnv))
	}

	// 修改返回的副本不应影响包内状态。
	for key := range got {
		delete(got, key)
	}
	got["FORGED"] = struct{}{}
	again := ReservedKeys()
	if len(again) != len(ModelFacingEnv) {
		t.Fatalf("修改返回值后 ReservedKeys() 长度 = %d, want %d", len(again), len(ModelFacingEnv))
	}
	if _, ok := again["FORGED"]; ok {
		t.Fatal("修改返回值污染了后续 ReservedKeys() 的结果")
	}
	if _, ok := again["NO_COLOR"]; !ok {
		t.Fatal("修改返回值丢失了受管键 NO_COLOR")
	}
}

// TestApplyModelEnv 以表驱动方式覆盖覆盖、保序、去重、保留键不可覆盖、
// CJK 与空输入等核心行为。期望值中的受管键按 GIT_PAGER/NO_COLOR/PAGER/TERM
// 排序出现在末尾（base 中已出现的受管键保持首次位置）。
func TestApplyModelEnv(t *testing.T) {
	managedTail := []string{"GIT_PAGER=cat", "NO_COLOR=1", "PAGER=cat", "TERM=dumb"}

	cases := []struct {
		name  string
		base  []string
		extra map[string]string
		want  []string
	}{
		{
			name: "空输入仍注入受管键",
			base: nil, extra: nil,
			want: managedTail,
		},
		{
			name: "覆盖 base 中的受管键",
			base: []string{"NO_COLOR=0", "TERM=xterm-256color", "FOO=bar"},
			want: []string{"NO_COLOR=1", "TERM=dumb", "FOO=bar", "GIT_PAGER=cat", "PAGER=cat"},
		},
		{
			name: "保序去重后写覆盖先写",
			base: []string{"A=1", "B=2", "A=3", "C=4"},
			want: append([]string{"A=3", "B=2", "C=4"}, managedTail...),
		},
		{
			name:  "extra 覆盖非保留键并追加新键",
			base:  []string{"A=1"},
			extra: map[string]string{"A": "9", "B": "2"},
			want:  append([]string{"A=9"}, append(append([]string{}, managedTail...), "B=2")...),
		},
		{
			name:  "extra 不得覆盖受管保留键",
			base:  []string{"A=1", "NO_COLOR=0"},
			extra: map[string]string{"A": "9", "NO_COLOR": "0", "PAGER": "less", "B": "2"},
			want:  []string{"A=9", "NO_COLOR=1", "GIT_PAGER=cat", "PAGER=cat", "TERM=dumb", "B=2"},
		},
		{
			name: "非法条目原样保留",
			base: []string{"BAD", "=emptykey", "EQUALS=a=b"},
			want: append([]string{"BAD", "=emptykey", "EQUALS=a=b"}, managedTail...),
		},
		{
			name: "CJK 键与值原样保留",
			base: []string{"中文键=中文值", "路径=/tmp/文件"},
			want: append([]string{"中文键=中文值", "路径=/tmp/文件"}, managedTail...),
		},
		{
			name:  "extra 中受管键与 base 同名也只保留规定值",
			base:  []string{"TERM=linux"},
			extra: map[string]string{"TERM": "screen", "NO_COLOR": ""},
			want:  []string{"TERM=dumb", "GIT_PAGER=cat", "NO_COLOR=1", "PAGER=cat"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ApplyModelEnv(tc.base, tc.extra)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ApplyModelEnv() =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// TestApplyModelEnvPure 校验纯函数约定：不修改 base 与 extra，
// 且重复调用结果一致。
func TestApplyModelEnvPure(t *testing.T) {
	base := []string{"NO_COLOR=0", "A=1", "A=2", "中文=值"}
	extra := map[string]string{"A": "9", "B": "2", "NO_COLOR": "0"}
	baseCopy := append([]string(nil), base...)
	extraCopy := make(map[string]string, len(extra))
	for k, v := range extra {
		extraCopy[k] = v
	}

	first := ApplyModelEnv(base, extra)
	second := ApplyModelEnv(base, extra)
	if !reflect.DeepEqual(base, baseCopy) {
		t.Fatalf("ApplyModelEnv 修改了 base: %q", base)
	}
	if !reflect.DeepEqual(extra, extraCopy) {
		t.Fatalf("ApplyModelEnv 修改了 extra: %v", extra)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("重复调用结果不一致: %q vs %q", first, second)
	}

	// 返回值与入参不共享底层数组。
	first[0] = "MUTATED=1"
	if base[0] == "MUTATED=1" {
		t.Fatal("返回值与 base 共享底层数组")
	}
}

// TestStripCredentials 以表驱动方式覆盖敏感剥离、大小写不敏感、
// 保序、CJK 无关、非法条目与空输入。
func TestStripCredentials(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want []string
	}{
		{
			name: "剥离常见敏感键并保留其余条目顺序",
			env: []string{
				"PATH=/bin",
				"OPENAI_API_KEY=sk-xxx",
				"GH_TOKEN=abc",
				"DB_PASSWORD=hunter2",
				"my_secret=1",
				"MONKEY=banana",
				"FOO=bar",
				"PASSWORD=root",
				"CREDENTIAL=x",
			},
			want: []string{"PATH=/bin", "MONKEY=banana", "FOO=bar"},
		},
		{
			name: "大小写不敏感",
			env:  []string{"token=x", "Api_Key=y", "PassWord=z", "My_SeCrEt=w", "keep=me"},
			want: []string{"keep=me"},
		},
		{
			name: "CJK 键名不受影响",
			env:  []string{"中文密钥=值", "OPENAI_API_KEY=中文", "路径=/文件"},
			want: []string{"中文密钥=值", "路径=/文件"},
		},
		{
			name: "值中无等号且键敏感",
			env:  []string{"API_TOKEN", "NO_EQUALS_HERE", "=badkey", "PASSWD=abc"},
			want: []string{"NO_EQUALS_HERE", "=badkey"},
		},
		{
			name: "空输入",
			env:  nil,
			want: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripCredentials(tc.env)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("StripCredentials() =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// TestStripCredentialsPure 校验 StripCredentials 不修改入参。
func TestStripCredentialsPure(t *testing.T) {
	env := []string{"PATH=/bin", "API_KEY=secret", "FOO=bar"}
	copyEnv := append([]string(nil), env...)
	got := StripCredentials(env)
	if !reflect.DeepEqual(env, copyEnv) {
		t.Fatalf("StripCredentials 修改了入参: %q", env)
	}
	got[0] = "MUTATED=1"
	if env[0] == "MUTATED=1" {
		t.Fatal("返回值与入参共享底层数组")
	}
}
