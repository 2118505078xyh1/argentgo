# Verify the artifacts produced by build-release.ps1 before they are published.
#
#   powershell -ExecutionPolicy Bypass -File scripts/verify-release.ps1 `
#       [-ArtifactDir dist/release] [-Version 0.5.0]
#
# -Version defaults to the Version constant in internal/cli/cli.go, the same
# single source of truth build-release.ps1 uses. Exits non-zero with a clear
# message on any mismatch; temporary extraction files are removed in finally.
# Standard-library PowerShell only; no external modules.

param(
    [string]$ArtifactDir = '',
    [string]$Version = ''
)

$ErrorActionPreference = 'Stop'

# Resolve paths relative to the repository, not the caller's working dir.
$repo = Split-Path -Parent $PSScriptRoot

function Get-ReleaseVersion([string]$Explicit) {
    if ($Explicit) { return $Explicit }
    $cli = Get-Content -LiteralPath (Join-Path $repo 'internal/cli/cli.go') -Raw
    $match = [regex]::Match($cli, 'const\s+Version\s*=\s*"([^"]+)"')
    if (-not $match.Success) {
        throw 'cannot read Version from internal/cli/cli.go; pass -Version explicitly'
    }
    return $match.Groups[1].Value
}

function Assert-ReleaseArtifacts([string]$Dir, [string]$Ver) {
    if (-not $Dir) { $Dir = Join-Path $repo 'dist/release' }
    $Dir = [IO.Path]::GetFullPath($Dir)
    $Ver = Get-ReleaseVersion $Ver
    $zipName = "argentgo_${Ver}_windows_amd64.zip"
    $zip = Join-Path $Dir $zipName
    $sums = Join-Path $Dir 'checksums.txt'

    if (-not (Test-Path -LiteralPath $Dir -PathType Container)) {
        throw "artifact directory does not exist: $Dir"
    }
    if (-not (Test-Path -LiteralPath $zip -PathType Leaf)) {
        throw "missing release zip: $zip"
    }
    Write-Host "  zip found: $zipName"

    if (-not (Test-Path -LiteralPath $sums -PathType Leaf)) {
        throw "missing checksums.txt: $sums"
    }
    Write-Host '  checksums.txt found'

    # Expand into a private temp dir and assert both shipped binaries are
    # present. Expand-Archive is part of the PowerShell standard library.
    $unpack = Join-Path ([IO.Path]::GetTempPath()) ('argentverify-' + [guid]::NewGuid().ToString('N'))
    try {
        New-Item -ItemType Directory -Force -Path $unpack | Out-Null
        Expand-Archive -LiteralPath $zip -DestinationPath $unpack -Force
        $names = @(Get-ChildItem -LiteralPath $unpack -Recurse -File | ForEach-Object { $_.Name })
        foreach ($required in @('argentgo.exe', 'argentgo-native.exe')) {
            if ($names -notcontains $required) {
                throw "release zip $zipName does not contain $required"
            }
        }
        Write-Host '  zip contains argentgo.exe and argentgo-native.exe'
    }
    finally {
        # Never leave extracted binaries behind, even on a failed assertion.
        if (Test-Path -LiteralPath $unpack) {
            Remove-Item -LiteralPath $unpack -Recurse -Force
        }
    }

    # checksums.txt is sha256sum-style: "<hex>  <file>". Find this zip's entry
    # and compare it with a freshly computed SHA-256 of the actual bytes.
    $expected = @()
    foreach ($line in @(Get-Content -LiteralPath $sums)) {
        if (-not $line.Trim()) { continue }
        $parts = $line -split '\s+', 2
        if ($parts.Count -lt 2) { throw "malformed checksums.txt line: $line" }
        $name = $parts[1].Trim().TrimStart('*')
        if ($name -eq $zipName) { $expected += $parts[0].Trim().ToLowerInvariant() }
    }
    if ($expected.Count -eq 0) { throw "checksums.txt has no entry for $zipName" }
    if ($expected.Count -gt 1) { throw "checksums.txt has multiple entries for $zipName" }
    if ($expected[0] -notmatch '^[0-9a-f]{64}$') {
        throw "invalid SHA-256 for $zipName in checksums.txt: $($expected[0])"
    }
    $actual = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected[0]) {
        throw "SHA-256 mismatch for $zipName`: checksums.txt=$($expected[0]), actual=$actual"
    }
    Write-Host "  SHA-256 verified: $actual"
    Write-Host "release artifacts OK: $zipName (version $Ver)"
}

try {
    Assert-ReleaseArtifacts -Dir $ArtifactDir -Ver $Version
}
catch {
    Write-Host "verify-release FAILED: $($_.Exception.Message)" -ForegroundColor Red
    exit 1
}
exit 0
