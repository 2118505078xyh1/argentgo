# Scan tracked files for high-confidence secret markers before publishing.
#
#   powershell -ExecutionPolicy Bypass -File scripts/privacy-scan.ps1 [-Filter '*.go']
#
# Deliberately narrow: only token prefixes and private-key headers, so examples
# in docs/tests do not block a release. Exits non-zero when anything matches.

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
        @{ name = 'private-key';    re = '-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----' }
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
