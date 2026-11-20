package tui

import (
	"reflect"
	"testing"
	"unicode/utf8"
)

// TestPreferencesFromModelEachField 校验三个字段各自独立落位，布尔开关
// 不会被调换，主题名按原始字符串保存。
func TestPreferencesFromModelEachField(t *testing.T) {
	for _, tc := range []struct {
		name    string
		theme   string
		compact bool
		noColor bool
		want    UIPreferences
	}{
		{"全为真", "dark", true, true, UIPreferences{Theme: "dark", CompactMode: true, NoColor: true}},
		{"全为假", "light", false, false, UIPreferences{Theme: "light"}},
		{"仅紧凑", "ansi", true, false, UIPreferences{Theme: "ansi", CompactMode: true}},
		{"仅无颜色", "dark", false, true, UIPreferences{Theme: "dark", NoColor: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := preferencesFromModel(tc.theme, tc.compact, tc.noColor)
			if got != tc.want {
				t.Fatalf("preferencesFromModel(%q, %v, %v) = %+v, want %+v",
					tc.theme, tc.compact, tc.noColor, got, tc.want)
			}
		})
	}
}

// TestPreferencesToMapEachField 校验映射包含全部稳定键，且键与值的类型
// 正确（主题为 string，两个开关为 bool），不因零值而丢键。
func TestPreferencesToMapEachField(t *testing.T) {
	want := map[string]any{
		uiPrefKeyTheme:       "light",
		uiPrefKeyCompactMode: true,
		uiPrefKeyNoColor:     false,
	}
	got := preferencesToMap(UIPreferences{Theme: "light", CompactMode: true, NoColor: false})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preferencesToMap = %#v, want %#v", got, want)
	}
	if len(got) != 3 {
		t.Fatalf("稳定字段数 = %d, want 3: %#v", len(got), got)
	}
	if _, ok := got[uiPrefKeyNoColor].(bool); !ok {
		t.Errorf("零值的 no_color 字段应为 bool，实际类型 %T", got[uiPrefKeyNoColor])
	}
}

// TestPreferencesCJKTheme 校验中文主题名按字节完整保留：原样写入映射、
// 合法 UTF-8、无截断，且稳定键本身不被污染。
func TestPreferencesCJKTheme(t *testing.T) {
	const theme = "深海夜色主题（自定义）"
	p := preferencesFromModel(theme, true, false)
	if p.Theme != theme {
		t.Fatalf("主题名被改写: got %q, want %q", p.Theme, theme)
	}
	m := preferencesToMap(p)
	stored, ok := m[uiPrefKeyTheme].(string)
	if !ok {
		t.Fatalf("主题字段类型 = %T, want string", m[uiPrefKeyTheme])
	}
	if stored != theme {
		t.Fatalf("映射中的主题名 = %q, want %q", stored, theme)
	}
	if !utf8.ValidString(stored) {
		t.Fatalf("主题名不是合法 UTF-8: %q", stored)
	}
	if len(m) != 3 {
		t.Fatalf("CJK 场景下字段数 = %d, want 3", len(m))
	}
	if got := preferencesFromMap(m); got.Theme != theme {
		t.Fatalf("还原的主题名 = %q, want %q", got.Theme, theme)
	}
}

// TestPreferencesRoundTrip 校验结构→映射→结构的完整往返：各种取值组合
// （含 CJK 主题名与布尔开关）经两次转换后与原始偏好逐字段一致。
func TestPreferencesRoundTrip(t *testing.T) {
	for _, want := range []UIPreferences{
		{},
		{Theme: "dark", CompactMode: true, NoColor: true},
		{Theme: "light", CompactMode: false, NoColor: true},
		{Theme: "深海夜色主题", CompactMode: true, NoColor: false},
	} {
		m := preferencesToMap(want)
		got := preferencesFromMap(m)
		if got != want {
			t.Errorf("往返丢失偏好: got %+v, want %+v (map=%#v)", got, want, m)
		}
	}
}

// TestPreferencesZeroValue 校验零值语义：零值结构编码出三个零值字段，
// 空映射与 nil 映射都还原为零值结构。
func TestPreferencesZeroValue(t *testing.T) {
	var zero UIPreferences
	m := preferencesToMap(zero)
	wantMap := map[string]any{
		uiPrefKeyTheme:       "",
		uiPrefKeyCompactMode: false,
		uiPrefKeyNoColor:     false,
	}
	if !reflect.DeepEqual(m, wantMap) {
		t.Fatalf("零值映射 = %#v, want %#v", m, wantMap)
	}
	if got := preferencesFromMap(m); got != zero {
		t.Fatalf("零值往返 = %+v, want %+v", got, zero)
	}
	for name, input := range map[string]map[string]any{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			if got := preferencesFromMap(input); got != zero {
				t.Fatalf("%s 映射还原 = %+v, want %+v", name, got, zero)
			}
		})
	}
}

// TestPreferencesFromMapMalformed 校验宽容性：键缺失或类型不符时回退到
// 零值而不 panic，且互不干扰其他字段。
func TestPreferencesFromMapMalformed(t *testing.T) {
	got := preferencesFromMap(map[string]any{
		uiPrefKeyTheme:       123,   // 类型不符
		uiPrefKeyCompactMode: "yes", // 类型不符
		uiPrefKeyNoColor:     true,  // 合法
	})
	want := UIPreferences{NoColor: true}
	if got != want {
		t.Fatalf("异常映射还原 = %+v, want %+v", got, want)
	}
}

// TestPreferencesStable 校验稳定性：相同输入多次编码结果一致，且映射的
// 键集合始终固定，不受 map 随机迭代顺序影响。
func TestPreferencesStable(t *testing.T) {
	p := UIPreferences{Theme: "深海主题", CompactMode: true, NoColor: true}
	first := preferencesToMap(p)
	for i := 0; i < 16; i++ {
		got := preferencesToMap(p)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("第 %d 次编码结果不一致: got %#v, want %#v", i+1, got, first)
		}
		if len(got) != 3 {
			t.Fatalf("第 %d 次编码字段数 = %d, want 3", i+1, len(got))
		}
	}
}
