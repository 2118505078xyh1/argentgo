# Reproducible security audit for argentgo. Runs fully offline: it never
# installs anything, and a missing govulncheck is a warning rather than a
# failure. Every other check is hard: any failure exits non-zero.
#
#   powershell -ExecutionPolicy Bypass -File scripts/security-audit.ps1
#
# Checks: go vet, gofmt, optional govulncheck, fuzz seed corpora, dependency
# allowlist, core stdlib-only import purity, and a privacy scan of tracked
# source. The dependency allowlist contains github.com/egoist/mygo and its
# pinned transitive tree because `go list -m all` reports those too; adding any
# other module is a deliberate, reviewed change to the list below.

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot

# AGENTS.md: the Windows toolchain is not always on PATH.
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    $goBin = Join-Path $env:USERPROFILE '.tools\go\go\bin'
    if (Test-Path (Join-Path $goBin 'go.exe')) { $env:Path = "$goBin;$env:Path" }
}

Push-Location $repo
try {
    $script:failures = [System.Collections.Generic.List[string]]::new()
    $script:checks = 0

    function Write-Step {
        param([string]$Name)
        Write-Host ''
        Write-Host "== $Name ==" -ForegroundColor Cyan
        $script:checks++
    }
    function Fail {
        param([string]$Message)
        $script:failures.Add($Message)
        Write-Host "FAIL: $Message" -ForegroundColor Red
    }
    function Invoke-Go {
        param([string[]]$Arguments, [string]$Label)
        & go @Arguments
        if ($LASTEXITCODE -ne 0) { throw "$Label exited with code $LASTEXITCODE" }
    }

    # 1. Static analysis.
    Write-Step 'go vet ./...'
    try {
        Invoke-Go @('vet', './...') 'go vet ./...'
        Write-Host 'ok'
    } catch { Fail $_.Exception.Message }

    # 2. Formatting: gofmt -l must print nothing.
    Write-Step 'gofmt -l .'
    try {
        $unformatted = @(& gofmt -l .)
        if ($LASTEXITCODE -ne 0) { throw "gofmt exited with code $LASTEXITCODE" }
        if ($unformatted.Count -gt 0) {
            $unformatted | ForEach-Object { Write-Host "  $_" }
            throw "$($unformatted.Count) file(s) are not gofmt-clean"
        }
        Write-Host 'ok: all files formatted'
    } catch { Fail $_.Exception.Message }

    # 3. Vulnerability scan. Optional by design so the audit works offline.
    Write-Step 'govulncheck ./... (optional)'
    if (Get-Command govulncheck -ErrorAction SilentlyContinue) {
        try {
            & govulncheck ./...
            if ($LASTEXITCODE -ne 0) { throw "govulncheck exited with code $LASTEXITCODE" }
            Write-Host 'ok'
        } catch { Fail $_.Exception.Message }
    } else {
        Write-Host 'SKIP: govulncheck not installed; install via: go install golang.org/x/vuln/cmd/govulncheck@latest' -ForegroundColor Yellow
    }

    # 4. Fuzz seed corpora only (bounded; no fuzzing engine needed).
    Write-Step 'go test ./internal/security -run Fuzz -count=1 (seeds only)'
    try {
        Invoke-Go @('test', './internal/security', '-run', 'Fuzz', '-count=1') 'fuzz seed test'
    } catch { Fail $_.Exception.Message }

    # 5. Dependency allowlist. The core must stay stdlib-only: the only direct
    #    third-party module is mygo (native UI); the rest are its pinned
    #    transitive dependencies reported by `go list -m all`.
    Write-Step 'go list -m all (dependency allowlist)'
    try {
        $mainLine = [string](@(& go list -m) | Select-Object -First 1)
        if ($LASTEXITCODE -ne 0) { throw "go list -m exited with code $LASTEXITCODE" }
        $mainName = ($mainLine -split '\s+')[0]

        $modLines = @(& go list -m all)
        if ($LASTEXITCODE -ne 0) { throw "go list -m all exited with code $LASTEXITCODE" }
        $names = @($modLines |
            ForEach-Object { ([string]$_ -split '\s+')[0] } |
            Where-Object { $_ -and $_ -ne $mainName } |
            Sort-Object -Unique)

        $allowed = @(
            'github.com/ProtonMail/go-crypto',
            'github.com/cloudflare/circl',
            'github.com/ebitengine/purego',
            'github.com/egoist/mygo',
            'github.com/go-text/typesetting',
            'github.com/go-text/typesetting-utils',
            'github.com/hajimehoshi/go-mp3',
            'github.com/icza/bitio',
            'github.com/jfreymuth/oggvorbis',
            'github.com/jfreymuth/vorbis',
            'github.com/klauspost/compress',
            'github.com/mewkiz/flac',
            'github.com/mewkiz/pkg',
            'github.com/mewpkg/term',
            'github.com/tphakala/go-aac',
            'github.com/tphakala/simd',
            'github.com/ulikunitz/xz',
            'go.digitalxero.dev/rpm',
            'golang.org/x/crypto',
            'golang.org/x/image',
            'golang.org/x/mod',
            'golang.org/x/net',
            'golang.org/x/sync',
            'golang.org/x/sys',
            'golang.org/x/term',
            'golang.org/x/text',
            'golang.org/x/tools'
        )

        $unexpected = @($names | Where-Object { $_ -notin $allowed })
        if ($unexpected.Count -gt 0) {
            $unexpected | ForEach-Object { Write-Host "  + $_" }
            Fail "unexpected module(s) in dependency tree: $($unexpected -join ', '). Review before adding to the allowlist."
        } elseif ($names -notcontains 'github.com/egoist/mygo') {
            Fail 'expected github.com/egoist/mygo is missing from the dependency tree'
        } else {
            Write-Host "ok: $($names.Count) third-party module(s), all allowlisted"
        }
    } catch { Fail $_.Exception.Message }

    # 5b. Enforce the invariant directly: no internal/ package may import a
    #     third-party module. Go's own vendored stdlib packages start with
    #     "vendor/" and therefore do not look like external modules.
    Write-Step 'go list -deps ./internal/... (stdlib-only core)'
    try {
        $deps = @(& go list -deps ./internal/...)
        if ($LASTEXITCODE -ne 0) { throw "go list -deps exited with code $LASTEXITCODE" }
        $external = @($deps | Where-Object {
            $_ -match '^[a-z0-9][a-z0-9.-]*\.[a-z]{2,}/' -and $_ -notlike "$mainName/*"
        })
        if ($external.Count -gt 0) {
            $external | ForEach-Object { Write-Host "  + $_" }
            Fail "internal/ packages import third-party code: $($external -join ', ')"
        } else {
            Write-Host 'ok: internal/ packages import only the standard library'
        }
    } catch { Fail $_.Exception.Message }

    # 6. Privacy scan of tracked source using Select-String. Two synthetic
    #    test fixtures are allowlisted by exact path + matched value; any new
    #    match, including elsewhere in the same file, is a hard failure.
    Write-Step 'privacy scan (tracked source)'
    try {
        $tracked = @(& git ls-files)
        if ($LASTEXITCODE -ne 0) { throw "git ls-files exited with code $LASTEXITCODE" }
        $files = @($tracked | Where-Object { $_ -match '\.(go|md|ps1|json|ya?ml|toml|txt|sh|js|ts|html|css)$' })

        $patterns = @(
            @{ name = 'openai-key';     re = 'sk-[A-Za-z0-9]{20,}' },
            @{ name = 'github-token';   re = 'gh[pousr]_[A-Za-z0-9]{36,}' },
            @{ name = 'aws-access-key'; re = 'AKIA[0-9A-Z]{16}' },
            @{ name = 'private-key';    re = '-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----' }
        )
        # The values are concatenated so this script does not itself contain a
        # contiguous token-like literal for its own privacy scan to flag.
        $allow = @(
            @{ path = 'internal/llm/secret_test.go'; value = 'sk-' + 'abcdefghijklmnopqrstuvwxyz'; why = 'synthetic redaction fixture' },
            @{ path = 'internal/tools/tools_security_scan_test.go'; value = 'AKIA' + 'IOSFODNN7EXAMPLE'; why = 'AWS documentation example key in scanner test' }
        )

        $hits = [System.Collections.Generic.List[string]]::new()
        $excusedCount = 0
        foreach ($file in $files) {
            $full = Join-Path $repo $file
            if (-not (Test-Path -LiteralPath $full)) { continue }
            if ((Get-Item -LiteralPath $full).Length -gt 2MB) { continue }
            foreach ($p in $patterns) {
                $matches = @(Select-String -LiteralPath $full -Pattern $p.re -AllMatches -CaseSensitive -ErrorAction SilentlyContinue)
                foreach ($m in $matches) {
                    foreach ($one in $m.Matches) {
                        $value = [string]$one.Value
                        $excused = $false
                        foreach ($a in $allow) {
                            if ($a.path -eq $file -and $value -ceq $a.value) {
                                $excused = $true
                                $excusedCount++
                                break
                            }
                        }
                        if (-not $excused) {
                            $hits.Add("$file`:$($m.LineNumber):$($p.name):$value")
                        }
                    }
                }
            }
        }
        if ($hits.Count -gt 0) {
            $hits | ForEach-Object { Write-Host "  $_" }
            Fail "privacy scan found $($hits.Count) potential secret(s)"
        } else {
            Write-Host "ok: $($files.Count) tracked source file(s) scanned ($excusedCount documented fixture(s) allowlisted)"
        }
    } catch { Fail $_.Exception.Message }

    Write-Host ''
    $code = 0
    if ($script:failures.Count -gt 0) {
        Write-Host "SECURITY AUDIT FAILED: $($script:failures.Count) of $script:checks check(s) failed" -ForegroundColor Red
        foreach ($f in $script:failures) { Write-Host "  - $f" }
        $code = 1
    } else {
        Write-Host "SECURITY AUDIT PASSED: $script:checks check(s) (govulncheck optional)" -ForegroundColor Green
    }
} finally {
    Pop-Location
}
exit $code
