package tools

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeGrepExFiles 在临时目录下写入若干文件，返回该目录的绝对路径。
func writeGrepExFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestGrepExContextBoundaries(t *testing.T) {
	dir := writeGrepExFiles(t, map[string]string{
		"a.txt": "alpha\nbeta\ngamma\ndelta\nepsilon\n",
	})

	for _, tc := range []struct {
		name       string
		pattern    string
		before     int
		after      int
		wantLine   int
		wantText   string
		wantBefore []string
		wantAfter  []string
	}{
		{
			name: "首行命中无前置", pattern: "alpha", before: 1, after: 1,
			wantLine: 1, wantText: "alpha", wantBefore: nil, wantAfter: []string{"beta"},
		},
		{
			name: "中间命中前后各一行", pattern: "gamma", before: 1, after: 1,
			wantLine: 3, wantText: "gamma", wantBefore: []string{"beta"}, wantAfter: []string{"delta"},
		},
		{
			name: "中间命中前后各两行", pattern: "gamma", before: 2, after: 2,
			wantLine: 3, wantText: "gamma", wantBefore: []string{"alpha", "beta"}, wantAfter: []string{"delta", "epsilon"},
		},
		{
			name: "尾行命中无后置", pattern: "epsilon", before: 1, after: 1,
			wantLine: 5, wantText: "epsilon", wantBefore: []string{"delta"}, wantAfter: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matches, files, err := GrepEx(GrepExOptions{
				Pattern:       tc.pattern,
				Path:          dir,
				Before:        tc.before,
				After:         tc.after,
				Literal:       true,
				CaseSensitive: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 1 {
				t.Fatalf("命中数 = %d，期望 1", len(matches))
			}
			m := matches[0]
			if m.Path != "a.txt" || m.Line != tc.wantLine || m.Text != tc.wantText {
				t.Fatalf("命中 = %+v，期望 path=a.txt line=%d text=%q", m, tc.wantLine, tc.wantText)
			}
			if !reflect.DeepEqual(m.Before, tc.wantBefore) {
				t.Fatalf("Before = %q，期望 %q", m.Before, tc.wantBefore)
			}
			if !reflect.DeepEqual(m.After, tc.wantAfter) {
				t.Fatalf("After = %q，期望 %q", m.After, tc.wantAfter)
			}
			if files["a.txt"] != 1 {
				t.Fatalf("files[a.txt] = %d，期望 1", files["a.txt"])
			}
		})
	}
}

func TestGrepExOutputModes(t *testing.T) {
	dir := writeGrepExFiles(t, map[string]string{
		"a.txt": "hit one\nmiss\nhit two\n",
		"b.txt": "hit three\n",
	})
	matches, files, err := GrepEx(GrepExOptions{
		Pattern: "hit", Path: dir, Literal: true, CaseSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := GrepExSummary(matches, files, grepExModeContent),
		"找到 3 处匹配，分布在 2 个文件：\n"+
			"a.txt:1: hit one\n"+
			"a.txt:3: hit two\n"+
			"b.txt:1: hit three"; got != want {
		t.Fatalf("content 摘要不符：\n%s\n期望：\n%s", got, want)
	}
	if got, want := GrepExSummary(matches, files, grepExModeFilesWithMatches),
		"共 2 个文件包含匹配：\na.txt\nb.txt"; got != want {
		t.Fatalf("files_with_matches 摘要不符：\n%s\n期望：\n%s", got, want)
	}
	if got, want := GrepExSummary(matches, files, grepExModeCount),
		"共 3 处匹配，分布在 2 个文件：\na.txt: 2\nb.txt: 1"; got != want {
		t.Fatalf("count 摘要不符：\n%s\n期望：\n%s", got, want)
	}
	if got := GrepExSummary(matches, files, "未知模式"); !strings.HasPrefix(got, "找到 3 处匹配") {
		t.Fatalf("未知模式应按 content 处理，实际：%q", got)
	}
}

func TestGrepExIncludeLiteralAndCase(t *testing.T) {
	dir := writeGrepExFiles(t, map[string]string{
		"a.go":     "Hello world\n",
		"b.txt":    "Hello there\n",
		"sub/c.go": "hello again\n",
		"lit.go":   "a.b and axb\n",
	})

	// include：只保留 *.go（含子目录，按文件名匹配）。
	matches, _, err := GrepEx(GrepExOptions{
		Pattern: "hello", Path: dir, Include: "*.go",
		Literal: true, CaseSensitive: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, m := range matches {
		paths = append(paths, m.Path)
	}
	// 不敏感模式下 a.go 的 Hello 与子目录 c.go 的 hello 都命中，
	// b.txt 被 include 排除，lit.go 没有 hello。
	if want := []string{"a.go", "sub/c.go"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("include 过滤后路径 = %q，期望 %q", paths, want)
	}

	// literal：'a.b' 只匹配字面量，不匹配 'axb'。
	matches, _, err = GrepEx(GrepExOptions{
		Pattern: "a.b", Path: dir, Include: "lit.go",
		Literal: true, CaseSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Text != "a.b and axb" {
		t.Fatalf("literal 命中 = %+v，期望仅命中字面量行", matches)
	}

	// case：敏感时大写 HELLO 不命中 text 文件内容里的小写 hello。
	matches, _, err = GrepEx(GrepExOptions{
		Pattern: "hello", Path: dir, Include: "b.txt",
		Literal: true, CaseSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("大小写敏感时不应命中，实际 %+v", matches)
	}
	matches, _, err = GrepEx(GrepExOptions{
		Pattern: "HELLO", Path: dir, Include: "b.txt",
		Literal: true, CaseSensitive: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Text != "Hello there" {
		t.Fatalf("大小写不敏感命中 = %+v", matches)
	}

	// 非法正则应返回带包装的错误。
	if _, _, err := GrepEx(GrepExOptions{Pattern: "[", Path: dir}); err == nil {
		t.Fatal("非法正则应返回错误")
	}
}

func TestGrepExHeadLimit(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 250; i++ {
		b.WriteString("hit line\n")
	}
	dir := writeGrepExFiles(t, map[string]string{"big.txt": b.String()})

	matches, files, err := GrepEx(GrepExOptions{
		Pattern: "hit", Path: dir, Literal: true, CaseSensitive: true, HeadLimit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("命中数 = %d，期望 2", len(matches))
	}
	if files["big.txt"] != len(matches) {
		t.Fatalf("计数与命中数不一致：%d vs %d", files["big.txt"], len(matches))
	}

	// <=0 用默认上限 200。
	matches, files, err = GrepEx(GrepExOptions{
		Pattern: "hit", Path: dir, Literal: true, CaseSensitive: true, HeadLimit: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != grepExDefaultHeadLimit || files["big.txt"] != grepExDefaultHeadLimit {
		t.Fatalf("默认上限命中 = %d / 计数 = %d，期望 %d", len(matches), files["big.txt"], grepExDefaultHeadLimit)
	}
}

func TestGrepExCJK(t *testing.T) {
	dir := writeGrepExFiles(t, map[string]string{
		"zh.txt": "第一行\n中文检索行\n第三行\n",
	})

	matches, _, err := GrepEx(GrepExOptions{
		Pattern: "中文", Path: dir, Context: 1, Literal: true, CaseSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("CJK 命中数 = %d，期望 1", len(matches))
	}
	m := matches[0]
	if m.Line != 2 || m.Text != "中文检索行" {
		t.Fatalf("CJK 命中 = %+v", m)
	}
	if !reflect.DeepEqual(m.Before, []string{"第一行"}) || !reflect.DeepEqual(m.After, []string{"第三行"}) {
		t.Fatalf("CJK 上下文 = %q / %q", m.Before, m.After)
	}

	// 正则模式下 CJK 同样安全。
	matches, _, err = GrepEx(GrepExOptions{
		Pattern: "检索", Path: dir, CaseSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Text != "中文检索行" {
		t.Fatalf("CJK 正则命中 = %+v", matches)
	}
}

func TestGrepExStableSort(t *testing.T) {
	dir := writeGrepExFiles(t, map[string]string{
		"z.txt":   "hit\n",
		"a.txt":   "hit\nhit\n",
		"m/b.txt": "hit\n",
	})
	matches, _, err := GrepEx(GrepExOptions{
		Pattern: "hit", Path: dir, Literal: true, CaseSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		path string
		line int
	}{
		{"a.txt", 1}, {"a.txt", 2}, {"m/b.txt", 1}, {"z.txt", 1},
	}
	if len(matches) != len(want) {
		t.Fatalf("命中数 = %d，期望 %d", len(matches), len(want))
	}
	for i, w := range want {
		if matches[i].Path != w.path || matches[i].Line != w.line {
			t.Fatalf("第 %d 项 = (%s,%d)，期望 (%s,%d)", i, matches[i].Path, matches[i].Line, w.path, w.line)
		}
	}
}

func TestGrepExDoesNotMutateInputs(t *testing.T) {
	dir := writeGrepExFiles(t, map[string]string{"a.txt": "zero\none hit two\nthree hit four\n"})
	opts := GrepExOptions{
		Pattern: "hit", Path: dir, Context: 1, Literal: true, CaseSensitive: true,
	}
	original := opts

	matches, files, err := GrepEx(opts)
	if err != nil {
		t.Fatal(err)
	}
	if opts != original {
		t.Fatalf("opts 被修改：%+v，原值 %+v", opts, original)
	}
	if len(matches) < 2 || len(matches[0].Before) == 0 {
		t.Fatalf("测试前置条件不成立：%+v", matches)
	}

	// 修改返回结果的 Context 切片，不应影响再次检索的结果。
	matches[0].Before[0] = "已修改"
	files["a.txt"] = -1
	again, againFiles, err := GrepEx(original)
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Before[0] == "已修改" || againFiles["a.txt"] != 2 {
		t.Fatalf("两次调用间存在共享状态：%+v / %v", again, againFiles)
	}
}

func TestGrepExEmptyInput(t *testing.T) {
	dir := writeGrepExFiles(t, nil) // 空目录
	matches, files, err := GrepEx(GrepExOptions{
		Pattern: "anything", Path: dir, Literal: true, CaseSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 || len(files) != 0 {
		t.Fatalf("空目录结果 = %+v / %v", matches, files)
	}
	if got := GrepExSummary(matches, files, grepExModeContent); got != "未找到匹配。" {
		t.Fatalf("空结果摘要 = %q", got)
	}

	// 有文件但无命中。
	dir = writeGrepExFiles(t, map[string]string{"a.txt": "no match here\n"})
	matches, files, err = GrepEx(GrepExOptions{
		Pattern: "zzz", Path: dir, Literal: true, CaseSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 || len(files) != 0 {
		t.Fatalf("无命中结果 = %+v / %v", matches, files)
	}

	// Path 为空时回退到 "."（仅验证不报错，不依赖具体工作目录内容）。
	if _, _, err := GrepEx(GrepExOptions{Pattern: "zzz-not-present-9f3a", Literal: true}); err != nil {
		t.Fatalf("Path 为空不应报错：%v", err)
	}
}
