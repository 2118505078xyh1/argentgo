# Check that cli.Version, MCP/LSP reported versions, an optional tag and CHANGELOG.md agree.
#
#   powershell -ExecutionPolicy Bypass -File scripts/check-release-consistency.ps1 [-Tag v0.1.1]
#
# Exits non-zero on any mismatch; used by the publisher and by release CI.

param(
    [string]$Tag = '',

    # Optional strict lower bound on the tracked .go line count. 0 means the
    # count is informational only. Deliberately NOT derived from cli.Version:
    # the version-anchor decision is still pending, so binding the two would
    # block releases unexpectedly. Pass this explicitly at release time once
    # the anchor policy is settled.
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

    # Code-size anchor, informational by default. Count newlines of tracked
    # .go files in PowerShell, mirroring the `git ls-files ... | wc -l` metric
    # in AGENTS.md without depending on bash/xargs. -MinLines turns it into a
    # strict, opt-in release gate.
    $goFiles = @(& git ls-files '*.go')
    if ($LASTEXITCODE -ne 0) { throw 'git ls-files *.go failed' }
    $totalLines = [long]0
    foreach ($goFile in $goFiles) {
        $full = Join-Path $repo $goFile
        if (-not (Test-Path -LiteralPath $full -PathType Leaf)) { continue }
        $text = [Text.Encoding]::UTF8.GetString([IO.File]::ReadAllBytes($full))
        $totalLines += [regex]::Matches($text, "`n").Count
    }
    Write-Host "tracked .go line count: $totalLines (informational; -MinLines enables strict gate)"
    if ($MinLines -gt 0 -and $totalLines -lt $MinLines) {
        throw "tracked .go line count $totalLines < required minimum $MinLines; code-size gate not met"
    }

    $shownTag = if ($Tag) { $Tag } else { '(any)' }
    Write-Host "release metadata consistent: cli.Version=$version, tag=$shownTag, CHANGELOG=v$version"
}
finally {
    Pop-Location
}
