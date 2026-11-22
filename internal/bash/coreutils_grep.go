package bash

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type grepFile struct {
	name     string // 展示名；标准输入为空
	data     string
	fromWalk bool // 来自 -r/-R 目录遍历
}

// biGrep 用 stdlib regexp 做真正的正则匹配（RE2/ERE），不再用 filepath.Match
// 拼 "*pat*" 冒充正则；未列出的选项一律报错。
func (it *Interp) biGrep(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "rnRvilEeFcq", "e")
	if err != nil {
		return it.failCmd("grep", 2, err)
	}
	pattern := ""
	files := p.operands
	if hasFlag(p, 'e') {
		pattern = p.flags['e']
	} else if len(files) > 0 {
		pattern, files = files[0], files[1:]
	}
	if pattern == "" {
		return it.failCmd("grep", 2, fmt.Errorf("missing pattern"))
	}
	pat := pattern
	if hasFlag(p, 'F') {
		pat = regexp.QuoteMeta(pat)
	}
	if hasFlag(p, 'i') {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat) // Go regexp 本身即 ERE，-E 是显式声明
	if err != nil {
		return it.failCmd("grep", 2, fmt.Errorf("invalid pattern %q: %w", pattern, err))
	}

	inputs, err := it.grepInputs(files, stdin, hasFlag(p, 'r') || hasFlag(p, 'R'))
	if err != nil {
		return it.failCmd("grep", 2, err)
	}
	showNames := len(inputs) > 1
	for _, in := range inputs {
		if in.fromWalk {
			showNames = true
		}
	}
	var out []string
	matchedAny := false
	for _, in := range inputs {
		count := 0
		for i, line := range splitLines(in.data) {
			hit := re.MatchString(line)
			if hasFlag(p, 'v') {
				hit = !hit
			}
			if !hit {
				continue
			}
			matchedAny = true
			count++
			if hasFlag(p, 'q') {
				it.LastStatus = 0
				return "", "", nil
			}
			if hasFlag(p, 'l') || hasFlag(p, 'c') {
				continue
			}
			var fields []string
			if showNames && in.name != "" {
				fields = append(fields, in.name)
			}
			if hasFlag(p, 'n') {
				fields = append(fields, strconv.Itoa(i+1))
			}
			fields = append(fields, line)
			out = append(out, strings.Join(fields, ":"))
		}
		if hasFlag(p, 'l') && count > 0 {
			name := in.name
			if name == "" {
				name = "(standard input)"
			}
			out = append(out, name)
		}
		if hasFlag(p, 'c') {
			if showNames {
				name := in.name
				if name == "" {
					name = "(standard input)"
				}
				out = append(out, name+":"+strconv.Itoa(count))
			} else {
				out = append(out, strconv.Itoa(count))
			}
		}
	}
	if !matchedAny {
		it.LastStatus = 1
		if hasFlag(p, 'c') {
			// GNU grep：无匹配也打印每个文件的 0 计数。
			return strings.Join(out, "\n") + "\n", "", nil
		}
		return "", "", nil
	}
	it.LastStatus = 0
	if len(out) == 0 {
		return "", "", nil
	}
	return strings.Join(out, "\n") + "\n", "", nil
}

// grepInputs 展开 grep 的文件操作数；-r/-R 递归遍历目录，所有路径走 resolvePath。
func (it *Interp) grepInputs(files []string, stdin string, recursive bool) ([]grepFile, error) {
	if len(files) == 0 {
		return []grepFile{{data: stdin}}, nil
	}
	var out []grepFile
	for _, f := range files {
		if f == "-" {
			out = append(out, grepFile{data: stdin})
			continue
		}
		abs, err := it.resolvePath(f)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if !info.IsDir() {
			data, err := os.ReadFile(abs)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			out = append(out, grepFile{name: f, data: string(data)})
			continue
		}
		if !recursive {
			return nil, fmt.Errorf("%s: is a directory (use -r)", f)
		}
		var walked []grepFile
		err = filepath.Walk(abs, func(path string, fi os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if _, err := it.resolvePath(path); err != nil {
				return err
			}
			if fi.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			rel, err := filepath.Rel(abs, path)
			if err != nil {
				return err
			}
			name := filepath.Join(f, rel)
			if f == "." {
				name = "./" + rel
			}
			walked = append(walked, grepFile{name: filepath.ToSlash(name), data: string(data), fromWalk: true})
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Slice(walked, func(i, j int) bool { return walked[i].name < walked[j].name })
		out = append(out, walked...)
	}
	return out, nil
}
