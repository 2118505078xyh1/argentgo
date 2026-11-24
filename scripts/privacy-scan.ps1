# Scan tracked files for high-confidence secret markers before publishing.
#
#   powershell -ExecutionPolicy Bypass -File scripts/privacy-scan.ps1 [-Filter '*.go']
#
# Three check families:
#   - secret markers (token prefixes and private-key blocks);
#   - local paths and e-mail addresses that betray the author's machine
#     (C:\Users\<name>, /home/<user>/, *@local);
#   - the same secret markers inside tests, where a leaked credential would
#     be just as real.
#
# Deliberately narrow, so examples in docs/tests do not block a release:
#   - the private-key pattern requires actual base64 key material after the
#     header, so prose/test references to the header alone do not match;
#   - an exact allowlist holds the public example key IDs and tokens that the
#     AWS / GitHub / OpenAI documentation publishes for exercises and the
#     redaction tests reuse. Exact match only: any real credential differs and
#     still fails the scan;
#   - placeholder form {{NAME_xxxxxxxx}} is not a credential and never fails;
#   - the local-path family only inspects non-test files, because test
#     fixtures legitimately contain fake paths such as C:\Users\tester.
#
# Approved exceptions live in scripts/privacy-scan.allowlist as
# "<file><TAB><fragment>": a matching line whose text contains the fragment
# is ignored. Exits non-zero when anything matches.

param([string]$Filter = '')

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
Push-Location $repo
try {
    $files = @(& git ls-files)
    if ($Filter) { $files = @($files | Where-Object { $_ -like $Filter }) }

    $patterns = @(
        @{ name = 'aws-access-key'; re = 'AKIA[0-9A-Z]{16}' },
        @{ name = 'github-token';   re = 'gh[pousr]_[A-Za-z0-9]{36,}' },
        @{ name = 'openai-key';     re = 'sk-[A-Za-z0-9]{20,}' },
        @{ name = 'private-key';    re = '-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----[A-Za-z0-9+/=\s]{100,}' }
    )

    # 本机路径与邮箱：只在非测试文件里检测（测试夹具里的假路径不算泄露）。
    $localPatterns = @(
        @{ name = 'local-windows-path'; re = '[A-Za-z]:\\Users\\[A-Za-z0-9._-]+' },
        @{ name = 'local-unix-path';    re = '/home/[a-z][a-z0-9._-]+/' },
        @{ name = 'local-mailbox';      re = '[A-Za-z0-9._%+-]+@local\b' }
    )

    # Public documentation example values (see header comment).
    $publicExamples = @(
        'AKIAIOSFODNN7EXAMPLE',
        'AKIAFAKESECRETKEY123',
        'ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD',
        'sk-abcdefghijklmnopqrstuvwxyz',
        'sk-testabcdef0123456789',
        'sk-anotherkey0123456789',
        'sk-abcdefghijklmnop1234'
    )

    # 已审定的例外：<file><TAB><fragment>。
    $allowPath = Join-Path $repo 'scripts/privacy-scan.allowlist'
    $allowed = @{}
    if (Test-Path -LiteralPath $allowPath) {
        foreach ($raw in Get-Content -LiteralPath $allowPath) {
            if ([string]::IsNullOrWhiteSpace($raw) -or $raw.TrimStart().StartsWith('#')) { continue }
            $parts = $raw -split "`t"
            if ($parts.Count -lt 2) { continue }
            $allowed["$($parts[0])`t$($parts[1])"] = $true
        }
    }

    # 扫描器自身与例外表按设计含有模式文本，跳过它们。
    $selfSkip = @('scripts/privacy-scan.ps1', 'scripts/privacy-scan.allowlist')

    $hits = [System.Collections.Generic.List[string]]::new()
    foreach ($file in $files) {
        if ($selfSkip -contains $file) { continue }
        $full = Join-Path $repo $file
        if (-not (Test-Path -LiteralPath $full)) { continue }
        $bytes = [IO.File]::ReadAllBytes($full)
        if ($bytes.Length -gt 2MB) { continue }
        $text = [Text.Encoding]::UTF8.GetString($bytes)
        $isTest = ($file -match '_test\.go$') -or ($file -match '(^|/)testdata/')
        $fragments = @($allowed.Keys | Where-Object { $_ -like "$file`t*" } | ForEach-Object { $_.Substring($file.Length + 1) })
        $lines = $text -split "`n"
        foreach ($pattern in $patterns) {
            foreach ($match in [regex]::Matches($text, $pattern.re)) {
                if ($publicExamples -contains $match.Value) { continue }
                $line = 1 + ([regex]::Matches($text.Substring(0, $match.Index), "`n")).Count
                if ($fragments | Where-Object { $lines[$line - 1].Contains($_) }) { continue }
                $hits.Add("$file`:$line`:$($pattern.name)")
            }
        }
        if ($isTest) { continue }
        for ($i = 0; $i -lt $lines.Count; $i++) {
            $lineText = $lines[$i]
            foreach ($pattern in $localPatterns) {
                if (-not [regex]::IsMatch($lineText, $pattern.re)) { continue }
                if ($fragments | Where-Object { $lineText.Contains($_) }) { continue }
                $hits.Add("$file`:$($i + 1)`:$($pattern.name)")
            }
        }
    }

    if ($hits.Count -gt 0) {
        foreach ($hit in $hits) { [Console]::Error.WriteLine($hit) }
        throw "privacy scan found $($hits.Count) potential secret(s)"
    }
    Write-Host "privacy scan clean: $($files.Count) tracked files"
}
finally {
    Pop-Location
}
