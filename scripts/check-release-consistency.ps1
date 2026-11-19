# Check that cli.Version, an optional tag and CHANGELOG.md agree.
#
#   powershell -ExecutionPolicy Bypass -File scripts/check-release-consistency.ps1 [-Tag v0.1.1]
#
# Exits non-zero on any mismatch; used by the publisher and by release CI.

param([string]$Tag = '')

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
Push-Location $repo
try {
    $cli = Get-Content -LiteralPath (Join-Path $repo 'internal/cli/cli.go') -Raw
    $match = [regex]::Match($cli, 'const\s+Version\s*=\s*"([^"]+)"')
    if (-not $match.Success) {
        throw 'cannot read Version from internal/cli/cli.go'
    }
    $version = $match.Groups[1].Value

    if ($Tag) {
        $tagVersion = $Tag -replace '^v', ''
        if ($tagVersion -ne $version) {
            throw "tag $Tag != cli.Version $version; bump the source before tagging"
        }
    }

    $changelog = Get-Content -LiteralPath (Join-Path $repo 'CHANGELOG.md') -Raw
    if ($changelog -notmatch "(?m)^##\s+v$([regex]::Escape($version))\b") {
        throw "CHANGELOG.md has no section for v$version"
    }

    $shownTag = if ($Tag) { $Tag } else { '(any)' }
    Write-Host "release metadata consistent: cli.Version=$version, tag=$shownTag, CHANGELOG=v$version"
}
finally {
    Pop-Location
}
