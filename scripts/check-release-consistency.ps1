# Check that cli.Version, MCP/LSP reported versions, an optional tag and CHANGELOG.md agree.
#
#   powershell -ExecutionPolicy Bypass -File scripts/check-release-consistency.ps1 [-Tag v0.1.1]
#
# Exits non-zero on any mismatch; used by the publisher and by release CI.

param(
    [string]$Tag = '',

    # Optional strict lower bound on the tracked .go line count. The script
    # also derives a minimum from the declared version->anchor map (single
    # source of truth in AGENTS.md); -MinLines can raise that bar further.
    [long]$MinLines = 0
)

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

    # Guard the version literals reported by the MCP/LSP clients, so bumping only cli.Version cannot slip through.
    $versionSources = @(
        @{ Path = 'internal/mcp/mcp.go';    Pattern = 'clientVersion\s*=\s*"([^"]+)"' },
        @{ Path = 'internal/lsp/client.go'; Pattern = '"version"\s*:\s*"([^"]+)"' }
    )
    foreach ($src in $versionSources) {
        $content = Get-Content -LiteralPath (Join-Path $repo $src.Path) -Raw
        $reportedMatch = [regex]::Match($content, $src.Pattern)
        if (-not $reportedMatch.Success) {
            throw "cannot read version literal from $($src.Path)"
        }
        $reported = $reportedMatch.Groups[1].Value
        if ($reported -ne $version) {
            throw "$($src.Path) reports version $reported != cli.Version $version; keep all version literals in sync"
        }
    }

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

    # Code-size anchor. Count newlines of tracked .go files in PowerShell,
    # mirroring the `git ls-files ... | wc -l` metric in AGENTS.md without
    # depending on bash/xargs. The version->threshold map below is the single
    # source of truth (kept in sync with AGENTS.md); -MinLines can still raise
    # the bar explicitly. When a version has no declared anchor, the gate is
    # informational only.
    $anchors = @{
        '0.8.1' = 300000
        '0.8.2' = 500000
        '0.8.3' = 650000
        '0.8.4' = 800000
        '0.9.0' = 900000
        '0.9.1' = 950000
        '1.0.0' = 1000000
        '1.1.0' = 1100000
    }
    $required = [long]0
    if ($anchors.ContainsKey($version)) { $required = [long]$anchors[$version] }
    if ($MinLines -gt $required) { $required = $MinLines }

    $goFiles = @(& git ls-files '*.go')
    if ($LASTEXITCODE -ne 0) { throw 'git ls-files *.go failed' }
    $totalLines = [long]0
    foreach ($goFile in $goFiles) {
        $full = Join-Path $repo $goFile
        if (-not (Test-Path -LiteralPath $full -PathType Leaf)) { continue }
        $text = [Text.Encoding]::UTF8.GetString([IO.File]::ReadAllBytes($full))
        $totalLines += [regex]::Matches($text, "`n").Count
    }
    if ($required -gt 0) {
        Write-Host "tracked .go line count: $totalLines (required for v${version}: $required)"
        if ($totalLines -lt $required) {
            throw "tracked .go line count $totalLines < required minimum $required for v$version; code-size gate not met"
        }
    } else {
        Write-Host "tracked .go line count: $totalLines (informational; no anchor for v$version)"
    }

    $shownTag = if ($Tag) { $Tag } else { '(any)' }
    Write-Host "release metadata consistent: cli.Version=$version, tag=$shownTag, CHANGELOG=v$version"
}
finally {
    Pop-Location
}
