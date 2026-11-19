# Extract a CHANGELOG section as GitHub Release notes.
#
#  powershell -ExecutionPolicy Bypass -File scripts/extract-release-notes.ps1 `
#      -Tag v0.1.1 -OutFile release-notes.md
#
# Without -OutFile the notes are written to stdout. The section body excludes
# the `## vX.Y.Z` heading; a missing or empty section is a hard failure.

param(
    [Parameter(Mandatory = $true)][string]$Tag,
    [string]$OutFile = ''
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$version = $Tag -replace '^v', ''
$lines = @(Get-Content -LiteralPath (Join-Path $repo 'CHANGELOG.md'))

$start = -1
for ($i = 0; $i -lt $lines.Count; $i++) {
    if ($lines[$i] -match "^\s*##\s+v$([regex]::Escape($version))\b") { $start = $i; break }
}
if ($start -lt 0) { throw "CHANGELOG.md has no section for v$version" }

$end = $lines.Count
for ($i = $start + 1; $i -lt $lines.Count; $i++) {
    if ($lines[$i] -match '^\s*##\s+') { $end = $i; break }
}
if ($end -le $start + 1) { throw "CHANGELOG section for v$version is empty" }

$body = ($lines[($start + 1)..($end - 1)] -join "`n").Trim()
if (-not $body) { throw "CHANGELOG section for v$version is empty" }

if ($OutFile) {
    $target = if ([IO.Path]::IsPathRooted($OutFile)) { $OutFile } else { Join-Path $repo $OutFile }
    [IO.File]::WriteAllText($target, $body + "`n", (New-Object Text.UTF8Encoding $false))
    Write-Host "wrote release notes: $target"
} else {
    Write-Output $body
}
