package agent

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

// TestParseCriticResult 覆盖合法/非法/缺失字段、分数钳制与 CJK 内容。
func TestParseCriticResult(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    CriticResult
		wantErr bool
	}{
		{
			name:  "完整合法对象",
			input: `{"score":0.42,"message":"不错","event_ids":["e1","e2"]}`,
			want:  CriticResult{Score: 0.42, Message: "不错", EventIDs: []string{"e1", "e2"}},
		},
		{
			name:  "只有 score",
			input: `{"score":0.5}`,
			want:  CriticResult{Score: 0.5},
		},
		{
			name:  "缺少 score 按零值",
			input: `{"message":"hi"}`,
			want:  CriticResult{Message: "hi"},
		},
		{
			name:  "全字段缺失",
			input: `{}`,
			want:  CriticResult{},
		},
		{
			name:  "CJK 评语与事件 ID 原样保留",
			input: `{"score":0.3,"message":"需要改进：补全测试","event_ids":["事件甲","事件乙"]}`,
			want:  CriticResult{Score: 0.3, Message: "需要改进：补全测试", EventIDs: []string{"事件甲", "事件乙"}},
		},
		{
			name:  "score 超过 1 钳制",
			input: `{"score":1.5}`,
			want:  CriticResult{Score: 1},
		},
		{
			name:  "score 为负钳制",
			input: `{"score":-2}`,
			want:  CriticResult{Score: 0},
		},
		{
			name:  "score 恰为 0 不变",
			input: `{"score":0}`,
			want:  CriticResult{Score: 0},
		},
		{
			name:  "score 恰为 1 不变",
			input: `{"score":1}`,
			want:  CriticResult{Score: 1},
		},
		{
			name:  "空 event_ids 保持为空切片",
			input: `{"event_ids":[]}`,
			want:  CriticResult{EventIDs: []string{}},
		},
		{
			name:  "event_ids 为 null 视为未提供",
			input: `{"event_ids":null}`,
			want:  CriticResult{},
		},
		{
			name:  "未知字段被忽略",
			input: `{"score":0.7,"metadata":{"k":1}}`,
			want:  CriticResult{Score: 0.7},
		},
		{name: "空字节", input: "", wantErr: true},
		{name: "仅空白", input: "   \n\t", wantErr: true},
		{name: "null", input: "null", wantErr: true},
		{name: "语法截断", input: `{"score":`, wantErr: true},
		{name: "尾部多余内容", input: `{"score":0.5} trailing`, wantErr: true},
		{name: "score 类型错误", input: `{"score":"0.5"}`, wantErr: true},
		{name: "event_ids 元素类型错误", input: `{"event_ids":[1,2]}`, wantErr: true},
		{name: "顶层为数组", input: `[1,2]`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCriticResult([]byte(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseCriticResult(%q) 期望错误，得到 %+v", tc.input, got)
				}
				if !errors.Is(err, ErrInvalidCriticResult) {
					t.Fatalf("错误未包装 ErrInvalidCriticResult: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCriticResult(%q) 意外错误: %v", tc.input, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseCriticResult(%q) = %+v, want %+v", tc.input, got, tc.want)
			}
		})
	}
}

// TestClampScore 验证 clampScore 对区间内、边界、无穷与 NaN 的处理。
func TestClampScore(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"区间内", 0.37, 0.37},
		{"零", 0, 0},
		{"一", 1, 1},
		{"负无穷", math.Inf(-1), 0},
		{"正无穷", math.Inf(1), 1},
		{"NaN 归零", math.NaN(), 0},
		{"极小负数", -0.0001, 0},
		{"略大于一", 1.0001, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampScore(tc.in); got != tc.want {
				t.Fatalf("clampScore(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestShouldRefine 覆盖分数/阈值、轮次/上限各边界，以及 max<=0 回退默认值。
func TestShouldRefine(t *testing.T) {
	cases := []struct {
		name          string
		score         float64
		threshold     float64
		iterations    int
		maxIterations int
		want          bool
	}{
		{"低分且未用尽轮次", 0.4, 0.7, 0, 3, true},
		{"分数恰等于阈值", 0.7, 0.7, 0, 3, false},
		{"分数高于阈值", 0.8, 0.7, 0, 3, false},
		{"轮次恰等于上限", 0.2, 0.7, 3, 3, false},
		{"轮次小于上限", 0.2, 0.7, 2, 3, true},
		{"轮次超过上限", 0.2, 0.7, 4, 3, false},
		{"默认上限：第 3 轮仍可细化", 0.2, 0.7, 2, 0, true},
		{"默认上限：第 4 轮停止", 0.2, 0.7, 3, 0, false},
		{"默认上限：负 max 同样回退", 0.2, 0.7, 2, -5, true},
		{"负轮次视为未开始", 0.2, 0.7, -1, 3, true},
		{"score 超范围钳制后高于阈值", 5, 0.9, 0, 3, false},
		{"score 为负钳制后低于阈值", -5, 0.1, 0, 3, true},
		{"NaN 分数归零后低于阈值", math.NaN(), 0.1, 0, 3, true},
		{"阈值为零时不细化", 0, 0, 0, 3, false},
		{"阈值为一：满分不细化", 1, 1, 0, 3, false},
		{"阈值为一：非满分细化", 0.9, 1, 0, 3, true},
		{"自定义上限为一：未用尽", 0.2, 0.7, 0, 1, true},
		{"自定义上限为一：已用尽", 0.2, 0.7, 1, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := CriticResult{Score: tc.score}
			if got := ShouldRefine(r, tc.threshold, tc.iterations, tc.maxIterations); got != tc.want {
				t.Fatalf("ShouldRefine(score=%v, threshold=%v, iterations=%d, max=%d) = %v, want %v",
					tc.score, tc.threshold, tc.iterations, tc.maxIterations, got, tc.want)
			}
		})
	}
}

// TestCriticResultEventIDsStable 验证 event_ids 顺序与重复项在解析后原样保留，可据此复现自评依据。
func TestCriticResultEventIDsStable(t *testing.T) {
	input := []byte(`  {"score":0.2,"message":"继续","event_ids":["事件三","事件一","事件三","事件二"]}  `)
	want := []string{"事件三", "事件一", "事件三", "事件二"}
	for i := 0; i < 3; i++ {
		got, err := ParseCriticResult(input)
		if err != nil {
			t.Fatalf("第 %d 次解析意外错误: %v", i+1, err)
		}
		if !reflect.DeepEqual(got.EventIDs, want) {
			t.Fatalf("第 %d 次解析 event_ids = %v, want %v", i+1, got.EventIDs, want)
		}
	}
}
