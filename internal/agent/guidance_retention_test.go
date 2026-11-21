package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// TestNudgePayloadDefaultMatchesBaseline 确认默认关闭时 nudge 载荷仍是原始字符串，
// 编码结果与历史 run 的会话记录逐字节一致。
func TestNudgePayloadDefaultMatchesBaseline(t *testing.T) {
	SetRetainGuidance(false)
	defer SetRetainGuidance(false)

	text := "换一种方法再试 — 中文"
	got := nudgePayload(text)
	if s, ok := got.(string); !ok || s != text {
		t.Fatalf("关闭时载荷 = %#v, want 原始字符串 %q", got, text)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(baseline) {
		t.Errorf("关闭时编码 = %s, want 基线 %s", data, baseline)
	}
}

// TestNudgePayloadRetained 确认 opt-in 开启时载荷携带 RetainInThread，
// 且 SurvivesCompaction 为真、不泄漏到子会话。
func TestNudgePayloadRetained(t *testing.T) {
	SetRetainGuidance(true)
	defer SetRetainGuidance(false)

	text := "连续错误，请先诊断根因"
	data, err := json.Marshal(nudgePayload(text))
	if err != nil {
		t.Fatal(err)
	}
	r := session.GetRetention(data)
	if r != session.RetainInThread {
		t.Fatalf("retention = %v, want RetainInThread", r)
	}
	if !r.SurvivesCompaction() {
		t.Errorf("RetainInThread 应能熬过压缩")
	}
	if r.SurvivesChildren() {
		t.Errorf("RetainInThread 不应传播到子会话")
	}
	if !strings.Contains(string(data), text) {
		t.Errorf("载荷缺少原文: %s", data)
	}
}

// TestNudgePayloadCJK 确认 CJK 文本原样保留、UTF-8 有效且不被修改。
func TestNudgePayloadCJK(t *testing.T) {
	SetRetainGuidance(true)
	defer SetRetainGuidance(false)

	text := "中文提示 — 保留原样"
	before := text
	data, err := json.Marshal(nudgePayload(text))
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(string(data)) {
		t.Errorf("载荷不是有效 UTF-8: %s", data)
	}
	if !strings.Contains(string(data), text) {
		t.Errorf("CJK 原文丢失: %s", data)
	}
	if text != before {
		t.Errorf("入参被修改: %q", text)
	}
}

// TestNudgePayloadStable 确认开关固定时重复渲染的载荷逐字节一致。
func TestNudgePayloadStable(t *testing.T) {
	for _, on := range []bool{false, true} {
		SetRetainGuidance(on)
		first, err := json.Marshal(nudgePayload("stable 提示"))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			got, err := json.Marshal(nudgePayload("stable 提示"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(first) {
				t.Fatalf("on=%v 第 %d 次载荷不稳定: %s vs %s", on, i, got, first)
			}
		}
	}
	SetRetainGuidance(false)
}

// TestInjectSteeringWithNudgeRecordsRetention 确认 stuck 提示的记录在 opt-in 开启时
// 带保留标记，关闭时与基线逐字节一致；普通 steering 始终保持原字符串。
func TestInjectSteeringWithNudgeRecordsRetention(t *testing.T) {
	type row struct {
		kind    string
		payload any
	}
	newState := func(rows *[]row) *runState {
		return &runState{rec: RecorderFunc(func(kind string, payload any) error {
			*rows = append(*rows, row{kind: kind, payload: payload})
			return nil
		})}
	}

	const guidance = "重复调用无效，换一种方法"

	// 开启：one-off 提示带保留标记。
	SetRetainGuidance(true)
	var onRows []row
	on := newState(&onRows)
	if !on.injectSteeringWithNudge(guidance) {
		t.Fatal("injectSteeringWithNudge 应报告已注入")
	}
	if len(onRows) != 1 || onRows[0].kind != "steer" {
		t.Fatalf("记录行 = %#v, want 单条 steer", onRows)
	}
	onData, err := json.Marshal(onRows[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := session.GetRetention(onData); got != session.RetainInThread {
		t.Errorf("开启时 retention = %v, want RetainInThread", got)
	}

	// 关闭：载荷与基线字符串逐字节一致。
	SetRetainGuidance(false)
	var offRows []row
	off := newState(&offRows)
	off.injectSteeringWithNudge(guidance)
	if len(offRows) != 1 {
		t.Fatalf("记录行 = %#v, want 单条 steer", offRows)
	}
	offData, err := json.Marshal(offRows[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := json.Marshal(guidance)
	if err != nil {
		t.Fatal(err)
	}
	if string(offData) != string(baseline) {
		t.Errorf("关闭时记录 = %s, want 基线 %s", offData, baseline)
	}

	// 普通 steering（非 nudge）即使开启也保持原字符串。
	SetRetainGuidance(true)
	var plainRows []row
	plain := newState(&plainRows)
	plain.injectSteering("普通纠正")
	if len(plainRows) != 1 {
		t.Fatalf("记录行 = %#v, want 单条 steer", plainRows)
	}
	plainData, err := json.Marshal(plainRows[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plainRows[0].payload, any("普通纠正")) {
		t.Errorf("普通 steering 载荷 = %#v, want 原字符串", plainRows[0].payload)
	}
	if session.GetRetention(plainData) != session.RetainDefault {
		t.Errorf("普通 steering 不应带保留标记: %s", plainData)
	}
	SetRetainGuidance(false)
}
