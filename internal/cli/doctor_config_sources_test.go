package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestDescribeConfigSources(t *testing.T) {
	tests := []struct {
		name    string
		sources []string
		key     string
		want    string
	}{
		{
			name:    "nil sources returns empty string",
			sources: nil,
			key:     "model",
			want:    "",
		},
		{
			name:    "empty sources returns empty string",
			sources: []string{},
			key:     "model",
			want:    "",
		},
		{
			name:    "single source",
			sources: []string{"/home/u/.argentgo/config.json"},
			key:     "model",
			want: "配置来源链 [model]（优先级由低到高，共 1 层）:\n" +
				"  1. model 来自 /home/u/.argentgo/config.json",
		},
		{
			name: "multiple sources keep precedence order",
			sources: []string{
				"/home/u/.argentgo/config.json",
				"/proj/.argent/config.json",
				"/proj/argent.json",
			},
			key: "baseURL",
			want: "配置来源链 [baseURL]（优先级由低到高，共 3 层）:\n" +
				"  1. baseURL 来自 /home/u/.argentgo/config.json\n" +
				"  2. baseURL 来自 /proj/.argent/config.json\n" +
				"  3. baseURL 来自 /proj/argent.json",
		},
		{
			name: "CJK key and paths stay intact",
			sources: []string{
				`C:\用户\配置\配置.json`,
				"/项目/argent.json",
			},
			key: "模型",
			want: "配置来源链 [模型]（优先级由低到高，共 2 层）:\n" +
				`  1. 模型 来自 C:\用户\配置\配置.json` + "\n" +
				"  2. 模型 来自 /项目/argent.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeConfigSources(tt.sources, tt.key)
			if got != tt.want {
				t.Fatalf("describeConfigSources() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestDescribeConfigSourcesIsStableAndDoesNotMutate(t *testing.T) {
	// 顺序稳定性：重复调用输出一致，且层顺序与传入顺序（低→高）相同。
	sources := []string{"low.json", "mid.json", "high.json"}
	original := append([]string(nil), sources...)

	first := describeConfigSources(sources, "key")
	second := describeConfigSources(sources, "key")
	if first != second {
		t.Fatalf("repeated calls differ:\n%s\nvs\n%s", first, second)
	}
	if !reflect.DeepEqual(sources, original) {
		t.Fatalf("input mutated: got %v, want %v", sources, original)
	}

	lowIdx := strings.Index(first, "low.json")
	midIdx := strings.Index(first, "mid.json")
	highIdx := strings.Index(first, "high.json")
	if lowIdx < 0 || midIdx < 0 || highIdx < 0 || !(lowIdx < midIdx && midIdx < highIdx) {
		t.Fatalf("layers out of order: %s", first)
	}
}
