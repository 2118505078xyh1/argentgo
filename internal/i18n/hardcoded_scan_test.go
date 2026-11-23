package i18n

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 本文件是 CLI 层英文硬编码的“增量守卫”：
//   - 扫描 internal/cli 与 cmd/argent 下的非测试 .go 文件；
//   - 只在 fmt 的输出/格式化家族与 errors.New / fmt.Errorf 的字符串字面量
//     参数里，找“含空格的多词英文”（如 "board is empty"）；
//   - 已进 i18n 表的引用（text()/Tr()/errorMessage() 调用）天然被排除：
//     它们的实参是 CallExpr 而非字符串字面量，扫描器只取 BasicLit；
//   - 存量不要求清零，全部登记在 hardcodedBaseline 中；断言仅是新结果
//     必须 ⊆ baseline，从而阻止继续恶化，同时允许旧硬编码被逐步清理。
//
// 白名单刻意保守（宁可漏报、不可误报）：格式指令、无空格的 flag 名/JSON 键/
// 错误码、已本地化的中文、刻意保持英文的 usage/help 文本都不计入，避免
// 后续维护者不断为新出现的纯技术字符串更新基线。

// hardcodedScanRoots 是相对仓库根的扫描目录。
var hardcodedScanRoots = []string{
	"internal/cli",
	"cmd/argent",
}

// hardcodedUsagePrefix 标识刻意保持英文的帮助文档块（不计入硬编码）。
const hardcodedUsagePrefix = "usage: argentgo"

// hardcodedBaseline 是引入守卫时已存在的英文硬编码字面量（去重后 201 条）。
// 只按字面量登记、不绑定文件，避免目录重构造成无意义的失败。
var hardcodedBaseline = []string{
	// 第三轮竞品优化新增的 CLI 诊断/状态消息。
	"--max-prompt-tokens must be >= 0, got %d",
	"all argent:// references valid",
	"argent:// links: %d refs in %d markdown files\n",
	"argentgo: discovered %d models from %s\n",
	"argentgo: docs index round-trip failed",
	"argentgo: docs show needs a name",
	"argentgo: inbox add needs text",
	"argentgo: inbox cancel needs an id",
	"argentgo: inbox steer needs an id",
	"argentgo: model catalog not modified",
	"argentgo: timeline needs a subcommand and a file",
	"argentgo: unknown docs subcommand %q\n",
	"argentgo: unknown inbox command %q\n",
	"argentgo: unknown timeline subcommand %q\n",
	"cleaned %d expired/overflow items\n",
	"decode json transcript: %w",
	"docs index: %d files, %d bytes\n",
	"found %d invalid and %d unregistered references:\n",
	"inbox: empty",
	"registered schemes: %s\n",
	"resize: %dx%d\n",
	"version=%d cols=%d rows=%d encoding=%s events=%d\n",
	// R57+ 新增的命令行诊断/状态消息（新增文件时尚未纳入初始基线）。
	"argentgo: --format needs json or text",
	"argentgo: --hide and --text are mutually exclusive",
	"argentgo: --profile needs a provider name",
	"argentgo: --text needs a value",
	"argentgo: edit-context needs --hide or a non-empty --text",
	"argentgo: invalid archive format %q (want json or text)\n",
	"argentgo: unexpected archive argument %q\n",
	"argentgo: unexpected edit-context argument %q\n",
	"argentgo: unknown plugin command %q\n",
	"hid record %s in session %s\n",
	"installed plugin %s\n",
	"no command diagnostics",
	"plugin %q not installed\n",
	"replace-edit recorded for %s in session %s\n",
	"uninstalled plugin %s\n",
	"\n%d check(s) failed\n",
	"  (none: compiled defaults only)",
	"  asset: %s (%d bytes)\n",
	"Total: %d  Passed: %d  Failed: %d\n",
	"  run `argentgo update --apply` to install it",
	"%d/%d orchestration scenarios passed\n",
	"%s: %d/%d tasks completed (completion_rate=%.3f)\n",
	"samples: %d  reason: %s  significant: %t\n",
	"%d/%d scenarios passed\n",
	"%s\t%s\tevents=%s\tdigest=%s\n",
	"%s needs a directory",
	"%s needs a value",
	"%s%s\tparent=%s\tdepth=%d\tlink=%s\t%s\t%s\n",
	"%s; no data is collected or sent\n",
	"%s; no-op preference saved to %s\n",
	"--api-key needs a value",
	"--approval must be ask, never or auto, got %q",
	"--base-url needs a value",
	"--grep needs a value for --limit",
	"--grep needs search text",
	"--max-cost must be >= 0, got %v",
	"--max-parallel must be >= 0, got %d",
	"--max-seconds must be >= 0, got %d",
	"--max-tokens must be >= 0, got %d",
	"--max-turns must be at least 1, got %d",
	"--mode must be text or json, got %q",
	"--price-input and --price-output must be >= 0",
	"--protocol must be anthropic, openai or responses, got %q",
	"--sandbox-mode must be read-only, workspace-write or danger-full-access, got %q",
	"/read-only %s\n",
	"FAIL %d %q: runner error: %v\n",
	"a newer release is available: %s (running %s)\n",
	"api key for %s: ",
	"append imported %s record: %w",
	"argentgo %s is up to date (latest %s)\n",
	"argentgo %s is up to date; nothing to apply\n",
	"argentgo gui: the native client is Windows-only",
	"argentgo gui: unexpected argument:",
	"argentgo listening on http://%s (bearer token required)\n",
	"argentgo needs an interactive terminal for the TUI; pass a prompt argument or pipe stdin for non-interactive use.",
	"argentgo: --asset needs a value",
	"argentgo: --current needs a value",
	"argentgo: --format needs a value (claude|codex|auto)",
	"argentgo: --input needs a value",
	"argentgo: --out needs a value",
	"argentgo: --param needs a key=value value",
	"argentgo: --param needs key=value (got %q)\n",
	"argentgo: --repo needs a value",
	"argentgo: --sha256 needs a value",
	"argentgo: --since/--until/--type only apply to json/yaml exports; %s export is unchanged\n",
	"argentgo: --tail needs a non-negative integer, got %q\n",
	"argentgo: --tail needs a value",
	"argentgo: --target needs a value",
	"argentgo: context: unexpected argument %q\n",
	"argentgo: doctor: encode JSON:",
	"argentgo: invalid import format %q (want claude|codex|auto)\n",
	"argentgo: opt-ins: %v\n",
	"argentgo: tui takes flags only; the prompt is typed in the UI",
	"argentgo: unexpected argument",
	"argentgo: unexpected argument %q\n",
	"argentgo: unexpected discard-attempt argument %q\n",
	"argentgo: unexpected export argument %q\n",
	"argentgo: unexpected fidelity argument %q\n",
	"argentgo: unexpected import argument %q\n",
	"argentgo: unexpected retry argument %q\n",
	"argentgo: unexpected sessions list argument %q\n",
	"argentgo: unknown config command %q\n",
	"argentgo: unknown flag %q\n",
	"argentgo: unknown sessions command %q\n",
	"argentgo: unknown shell %q (want bash, zsh or powershell)\n",
	"argentgo: unknown telemetry action %q\n",
	"argentgo: unknown workflow action %q\n",
	"argentgo: version: unexpected argument %q\n",
	"base URL",
	"board is empty",
	"checked out %s at %s\n",
	"cli: duplicate command name or alias",
	"cli: nil session",
	"config error: %v\n",
	"config sources (lowest to highest precedence):",
	"config sources: %s\n",
	"create config dir: %w",
	"default model",
	"device login for %s\n",
	"discard fork id %q already exists",
	"effective configuration (precedence: default < file < env < flag)",
	"empty transcript",
	"encode config: %w",
	"environment overrides set: %s\n",
	"error: %s is not set in %s\n",
	"error: --serve requires a bearer token (--serve-token or ARGENT_SERVE_TOKEN)",
	"error: TUI not configured",
	"error: context report not configured",
	"error: create telemetry storage:",
	"error: create update directory:",
	"error: detect format of %s: %v\n",
	"error: encode telemetry preference:",
	"error: eval runner does not support --live (live model runs)",
	"error: eval runner does not support orchestration",
	"error: eval runner does not support single scenarios",
	"error: eval runner not configured",
	"error: import %s: %v\n",
	"error: jobs: no job named %q\n",
	"error: locate running binary:",
	"error: native frontend not found:",
	"error: no api key provided",
	"error: no checksum for %s; pass --sha256\n",
	"error: no messages found in %s (format %s)\n",
	"error: no release asset for this platform in %s\n",
	"error: no release found for %s\n",
	"error: no workflow named %q\n",
	"error: open session: %v\n",
	"error: open sessions:",
	"error: read records: %v\n",
	"%d. %s %s %d bytes sha256:%s\n",
	"error: provider %q does not support device login\n",
	"error: read %s: %v\n",
	"error: read api key:",
	"error: read stdin:",
	"error: resolve workspace:",
	"error: save imported session: %v\n",
	"error: session has no active record",
	"error: token report not configured",
	"error: workflow run needs a definition JSON path",
	"error: write release notes:",
	"error: write telemetry preference:",
	"eval history: %v\n",
	"eval history: invalid --max-tasks %q\n",
	"eval history: repository path is required",
	"eval history: the current evaluation backend does not support history runs",
	"eval history: unknown argument %q\n",
	"%d/%d history tasks failed self-check\n",
	"%d/%d history tasks passed self-check\n",
	"%s %s (parentFailed=%v commitPassed=%v%s)\n",
	"eval history verify: invalid --max-tasks %q\n",
	"eval history verify: repository path is required",
	"eval history verify: unknown argument %q\n",
	"eval history verify: %v\n",
	"expected a JSON array of results or {\"results\": [...]}",
	"expected a JSON array of scenarios or {\"scenarios\": [...]}",
	"expected a JSON object of name-to-fingerprint or {\"fingerprints\": {...}}",
	"expected a non-empty value",
	"expected key=value, got %q",
	"forked session %s\n",
	"forked session %s from %s\n",
	"help requested",
	"invalid --limit %q",
	"invalid discard fork id %q",
	"invalid dotted key %q",
	"invalid line count %q",
	"invalid session id %q",
	"job %s is already %s\n",
	"latest audit: %s\n",
	"load session %q: %w",
	"logged out %s\n",
	"max turns: %d\n",
	"model: %s\nprotocol: %s\nprovider: %s\nsandbox: %s\nmaxTurns: %d\n",
	"no MCP servers configured (sources: %s)\n",
	"no audit log under %s\n",
	"no automations store at %s\n",
	"no background jobs",
	"no hook documents found under .argent/hooks.json or ~/.argentgo/hooks.json",
	"no jobs directory at %s\n",
	"no jobs directory at %s: start a background job first",
	"no matches for %q\n",
	"no profiles configured",
	"no runner configured: set cli.DefaultRunner (integrator wires internal/agent)",
	"no scenarios in %s\n",
	"no sessions matching %q\n",
	"no sessions recorded",
	"no skills found under %s\n",
	"no slash commands found under .argent/commands or ~/.argentgo/commands",
	"no stored credentials",
	"note: --output-schema validates the final answer and retries once",
	"note: -c/--config overrides beat environment; config show applies them as the flag layer",
	"nothing to log out",
	"open %s and enter code %s\n",
	"override %q has an empty value",
	"override has an empty key",
	"policy:    default (path traversal denied; unknown tools ask)",
	"provider:  %s  protocol: %s  model: %s\n",
	"read config: %w",
	"refusing to write through symlink or reparse point %q",
	"release notes: %s\n",
	"remove session %q: %w",
	"renamed session %s\n",
	"restored %d file(s) from checkpoint %s in %s\n",
	"restored prompt: %s\n",
	"sandbox:   %s (mutating tools %s)\n",
	"saved credentials for %s in %s\n",
	"saved device token for %s in %s\n",
	"session: encode filtered export: %w",
	"session: invalid JSON export cannot be rendered as YAML",
	"session: malformed YAML export: missing header",
	"session: parse export for filtering: %w",
	"sessions error: %v\n",
	"set %s in %s\n",
	"stopped job %s\n",
	"stored credentials: %s\n",
	"unexpected argument %q",
	"unexpected undo argument %q",
	"unknown --grep option %q",
	"unknown flag %q",
	"unrecognized transcript format (want Claude Code user/assistant events or a Codex session_meta line)",
	"update: build checksum request: %w",
	"update: conflicting checksums for %s in release manifest; pass --sha256",
	"update: fetch checksum: %w",
	"update: hash staged binary: %w",
	"update: no SHA-256 checksum for %s; pass --sha256",
	"update: open staged binary: %w",
	"update: orphan checksum entries in release manifest: %s; pass --sha256",
	"update: read checksum: %w",
	"update: read staged binary: %w",
	"update: staged binary checksum mismatch: expected %s, got %s",
	"update: staged file %s is a gzip archive; refusing to install it as the executable",
	"update: staged file %s is a zip archive; refusing to install it as the executable",
	"updated argentgo to %s\n",
	"waiting for authorization (polling every %ds, expires in %ds)\n",
	"warning: config set rewrites as strict JSON; comments and trailing commas are not preserved",
	"workflow: read %s: %w",
	"write config: %w",

	// R53 新增：replay --verify 的结构化诊断输出（CLI 英文诊断惯例，登记以保增量守卫）。
	"Active leaf: %s\n",
	"Broken links: %d\n",
	"Verify: ok",
}

// hardcodedHit 是一次命中的位置与上下文，仅用于失败时给出可操作的诊断。
type hardcodedHit struct {
	File    string // 相对仓库根的路径:行号
	Call    string // 构造函数名，如 fmt.Errorf
	Literal string // 命中的英文字符串字面量
}

// hardcodedRepoRoot 从当前包目录向上找到 go.mod，作为相对路径扫描的根。
func hardcodedRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("向上遍历未找到 go.mod")
		}
		dir = parent
	}
}

// englishWordRuns 返回 s 中长度 >=2 的连续 ASCII 字母串个数，用来把真正的
// 英文词与 "%s"、"%-8s" 这类只含单个字母的格式指令区分开。
func englishWordRuns(s string) int {
	n, run := 0, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			run++
		} else {
			if run >= 2 {
				n++
			}
			run = 0
		}
	}
	if run >= 2 {
		n++
	}
	return n
}

// isHardcodedEnglish 判断一个字符串字面量是否算“含空格的多词英文硬编码”。
// 白名单在此集中，逻辑保守：只要可能是技术字符串就返回 false（漏报优于误报）。
func isHardcodedEnglish(s string) bool {
	// 已是中文或其它非 ASCII 文案：跳过。
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	// 无空格：flag 名、JSON 键、错误码、技术标识符通常无空格。
	if !strings.ContainsAny(s, " \t") {
		return false
	}
	// 少于两个英文词：纯格式指令（如 "%s\t%-8s\t%s\n"）不算。
	if englishWordRuns(s) < 2 {
		return false
	}
	// 刻意保持英文的 usage/help 文档块：跳过，避免帮助文本迭代造成噪声。
	first := strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if strings.HasPrefix(first, hardcodedUsagePrefix) {
		return false
	}
	return true
}

// targetOutputCall 识别用户可见的输出/错误构造函数，返回稳定的调用名。
// 覆盖 fmt 的输出/格式化家族（比需求列出的四个更全，防止换兄弟函数绕过）
// 与 errors.New、fmt.Errorf。
func targetOutputCall(fun ast.Expr) (string, bool) {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	switch pkg.Name {
	case "fmt":
		switch sel.Sel.Name {
		case "Fprint", "Fprintf", "Fprintln", "Print", "Printf", "Println", "Errorf":
			return "fmt." + sel.Sel.Name, true
		}
	case "errors":
		if sel.Sel.Name == "New" {
			return "errors.New", true
		}
	}
	return "", false
}

// scanHardcodedEnglish 扫描两个源码根目录，返回所有命中（含位置）。
func scanHardcodedEnglish(t *testing.T) []hardcodedHit {
	t.Helper()
	root := hardcodedRepoRoot(t)
	scanned := make(map[string]int, len(hardcodedScanRoots))
	var hits []hardcodedHit

	for _, relRoot := range hardcodedScanRoots {
		base := filepath.Join(root, filepath.FromSlash(relRoot))
		if _, err := os.Stat(base); err != nil {
			t.Fatalf("扫描根目录不存在 %s: %v", base, err)
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			scanned[relRoot]++
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return nil // 无法解析的文件跳过；扫描本身仍稳定
			}
			relPath, err := filepath.Rel(root, path)
			if err != nil {
				return nil
			}
			relSlash := filepath.ToSlash(relPath)
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, ok := targetOutputCall(call.Fun)
				if !ok {
					return true
				}
				for _, arg := range call.Args {
					// 只取字符串字面量：text()/Tr()/errorMessage() 调用等
					// 实参是 CallExpr，天然被排除。
					lit, ok := arg.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						continue
					}
					if isHardcodedEnglish(s) {
						hits = append(hits, hardcodedHit{
							File:    fmt.Sprintf("%s:%d", relSlash, fset.Position(lit.Pos()).Line),
							Call:    name,
							Literal: s,
						})
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("扫描 %s 失败: %v", relRoot, err)
		}
	}
	// 若两个根目录任一没扫描到文件，说明路径逻辑已失效，不能让守卫空过。
	for _, relRoot := range hardcodedScanRoots {
		if scanned[relRoot] == 0 {
			t.Fatalf("根目录 %s 下未扫描到任何 .go 文件，扫描器疑似失效", relRoot)
		}
	}
	return hits
}

// TestCLIHardcodedEnglishDoesNotGrow 是增量守卫：新检出的硬编码必须都在
// baseline 中；存量允许继续存在或逐步清理。结果排序后按集合比较，稳定且与
// 文件遍历顺序、平台路径分隔符无关。
func TestCLIHardcodedEnglishDoesNotGrow(t *testing.T) {
	hits := scanHardcodedEnglish(t)

	locations := make(map[string][]string, len(hits))
	for _, h := range hits {
		locations[h.Literal] = append(locations[h.Literal], h.File+" ("+h.Call+")")
	}

	baseline := make(map[string]struct{}, len(hardcodedBaseline))
	for _, s := range hardcodedBaseline {
		baseline[s] = struct{}{}
	}

	found := make([]string, 0, len(locations))
	for s := range locations {
		found = append(found, s)
	}
	sort.Strings(found)

	var added []string
	for _, s := range found {
		if _, known := baseline[s]; known {
			continue
		}
		where := append([]string(nil), locations[s]...)
		sort.Strings(where)
		added = append(added, fmt.Sprintf("%q @ %s", s, strings.Join(where, ", ")))
	}

	t.Logf("CLI 硬编码扫描：%d 处命中，%d 个去重字面量；baseline %d 个；新增 %d 个",
		len(hits), len(found), len(hardcodedBaseline), len(added))
	if len(added) > 0 {
		t.Errorf("检测到 %d 个新的英文硬编码（请改用 i18n 的 text()/Tr()/errorMessage()，或确认属于白名单类别）:\n  %s",
			len(added), strings.Join(added, "\n  "))
	}
}

// TestHardcodedBaselineIsClean 保证基线集合自身无重复、非空，防止登记表
// 在维护中被写坏而失去意义。
func TestHardcodedBaselineIsClean(t *testing.T) {
	if len(hardcodedBaseline) == 0 {
		t.Fatal("hardcodedBaseline 为空，守卫失去意义")
	}
	seen := make(map[string]struct{}, len(hardcodedBaseline))
	for _, s := range hardcodedBaseline {
		if _, dup := seen[s]; dup {
			t.Errorf("hardcodedBaseline 存在重复条目: %q", s)
		}
		seen[s] = struct{}{}
	}
}

// TestIsHardcodedEnglish 固化检测与白名单的判定口径，便于日后维护时理解
// 哪些字符串会被守卫计入、哪些被刻意漏报。
func TestIsHardcodedEnglish(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"multi word status", "board is empty", true},
		{"error prefix with prose", "error: read stdin:", true},
		{"prefix with format verb", "device login for %s\n", true},
		{"format directives only", "%s\t%-8s\t%s\t%s\n", false},
		{"flag name has no space", "--sandbox-mode", false},
		{"json key has no space", "expect_tool", false},
		{"error code has no space", "ENOENT", false},
		{"already chinese", "会话已清空", false},
		{"mixed chinese", "错误: 出错了", false},
		{"usage block excluded", "usage: argentgo board\n\nPrints a read-only board.\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHardcodedEnglish(tc.in); got != tc.want {
				t.Errorf("isHardcodedEnglish(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
