# Scan tracked files for high-confidence secret markers before publishing.
#
#   powershell -ExecutionPolicy Bypass -File scripts/privacy-scan.ps1 [-Filter '*.go']
#
# Deliberately narrow: only token prefixes and private-key blocks, so examples
# in docs/tests do not block a release. Two carve-outs keep it that way without
# weakening the gate:
#   - the private-key pattern requires actual base64 key material after the
#     header, so prose/test references to the header alone do not match;
#   - an exact allowlist holds the public example key IDs and tokens that the
#     AWS / GitHub / OpenAI documentation publishes for exercises and the
#     redaction tests reuse. Exact match only: any real credential differs and
#     still fails the scan. Exits non-zero when anything matches.

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

    $hits = [System.Collections.Generic.List[string]]::new()
    foreach ($file in $files) {
        $full = Join-Path $repo $file
        if (-not (Test-Path -LiteralPath $full)) { continue }
        $bytes = [IO.File]::ReadAllBytes($full)
        if ($bytes.Length -gt 2MB) { continue }
        $text = [Text.Encoding]::UTF8.GetString($bytes)
        foreach ($pattern in $patterns) {
            foreach ($match in [regex]::Matches($text, $pattern.re)) {
                if ($publicExamples -contains $match.Value) { continue }
                $line = 1 + ([regex]::Matches($text.Substring(0, $match.Index), "`n")).Count
                $hits.Add("$file`:$line`:$($pattern.name)")
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
